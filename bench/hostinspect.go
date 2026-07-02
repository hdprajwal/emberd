package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// procRoot is the default root the host-inspection readers walk. It is a var,
// not a constant baked into the readers, so every reader stays a pure function
// of a path and can be tested against a fixture /proc tree (bench-v2-spec.md
// §10). The churn and memory scenarios are the only ones that inspect host
// state; all of that inspection funnels through the helpers here.
var procRoot = "/proc"

// firecrackerComm and daemonComm are the exact process names (comm, i.e.
// /proc/<pid>/comm) the churn and memory scenarios match on, the same names
// `pgrep -x firecracker` / `pgrep -x emberd` match (bench-v2-spec.md §6.6,
// §6.7).
const (
	firecrackerComm = "firecracker"
	daemonComm      = "emberd"
)

// findMatchingProcs returns the sorted PIDs under root whose comm
// (/proc/<pid>/comm) equals name exactly — the `pgrep -x <name>` semantics.
// Non-numeric directory names are skipped, as are processes that vanish
// mid-scan (a comm read that fails), so a racing exit never fails the walk.
func findMatchingProcs(root, name string) ([]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a /proc/<pid> directory
		}
		comm, err := os.ReadFile(filepath.Join(root, e.Name(), "comm"))
		if err != nil {
			continue // process exited between ReadDir and here
		}
		if strings.TrimRight(string(comm), "\n") == name {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

// countMatchingProcs returns how many processes under root have comm exactly
// equal to name — the `pgrep -c -x <name>` semantics the churn leak check uses
// (bench-v2-spec.md §6.6).
func countMatchingProcs(root, name string) (int, error) {
	pids, err := findMatchingProcs(root, name)
	if err != nil {
		return 0, err
	}
	return len(pids), nil
}

// countFDs returns the open file-descriptor count for pid: the entry count of
// /proc/<pid>/fd, the `ls /proc/<pid>/fd | wc -l` equivalent the churn fd-leak
// check uses (bench-v2-spec.md §6.6).
func countFDs(root string, pid int) (int, error) {
	entries, err := os.ReadDir(filepath.Join(root, strconv.Itoa(pid), "fd"))
	if err != nil {
		return 0, fmt.Errorf("count fds for pid %d: %w", pid, err)
	}
	return len(entries), nil
}

// readCmdline returns pid's full command line (/proc/<pid>/cmdline) with the
// NUL argument separators normalized to spaces, so a caller can substring-match
// a sandbox id embedded in the firecracker `--api-sock <workdir>/<id>/fc.sock`
// argument.
func readCmdline(root string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", fmt.Errorf("read cmdline for pid %d: %w", pid, err)
	}
	trimmed := strings.TrimRight(string(data), "\x00")
	return strings.ReplaceAll(trimmed, "\x00", " "), nil
}

// attributeFirecrackerPID finds the single firecracker process under root whose
// command line carries sandboxID (in its per-sandbox api-socket / work-dir
// path). Exactly one match is required: the memory scenario prefers this
// cmdline-based attribution over "new PID after create" PID-set diffing because
// the latter is unreliable under a warm pool — a create may pop a pre-existing
// pool VM while an unrelated refill VM appears, so a naive diff mis-attributes
// (bench-v2-spec.md §6.7, controller amendment). Zero or many matches is
// returned as an explicit ambiguity error so the caller fails loudly rather
// than attributing PSS to the wrong VM.
func attributeFirecrackerPID(root, sandboxID string) (int, error) {
	pids, err := findMatchingProcs(root, firecrackerComm)
	if err != nil {
		return 0, err
	}
	var matches []int
	for _, pid := range pids {
		cmdline, err := readCmdline(root, pid)
		if err != nil {
			continue // process exited mid-scan
		}
		if strings.Contains(cmdline, sandboxID) {
			matches = append(matches, pid)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return 0, fmt.Errorf("no firecracker process found for sandbox %s", sandboxID)
	default:
		return 0, fmt.Errorf("ambiguous attribution: %d firecracker processes match sandbox %s (%v)", len(matches), sandboxID, matches)
	}
}

// smapsRollupPath returns the /proc/<pid>/smaps_rollup path the memory scenario
// reads a VM's proportional set size from.
func smapsRollupPath(root string, pid int) string {
	return filepath.Join(root, strconv.Itoa(pid), "smaps_rollup")
}

// parsePSSKiB parses the `Pss:` line (value in kB, i.e. KiB) from a
// smaps_rollup-formatted file at path. The `Pss:` prefix match is exact, so it
// never picks up `Pss_Anon:` / `Pss_File:` / `Pss_Shmem:`. A missing, empty, or
// malformed Pss line is an error, never a silent zero — the memory scenario
// must fail loudly on unreadable/unparseable smaps rather than report a zero
// PSS (bench-v2-spec.md §6.7).
func parsePSSKiB(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read smaps_rollup %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Pss:") {
			continue
		}
		fields := strings.Fields(line) // e.g. ["Pss:", "6789", "kB"]
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed Pss line in %s: %q", path, line)
		}
		kib, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, fmt.Errorf("parse Pss value in %s (%q): %w", path, line, err)
		}
		return kib, nil
	}
	return 0, fmt.Errorf("no Pss line in %s", path)
}

// readPSSMiB reads a VM's proportional set size from a smaps_rollup file at
// path and converts it to MiB. It surfaces parsePSSKiB's error unchanged so an
// unreadable smaps fails the memory scenario loudly.
func readPSSMiB(path string) (float64, error) {
	kib, err := parsePSSKiB(path)
	if err != nil {
		return 0, err
	}
	return float64(kib) / 1024.0, nil
}

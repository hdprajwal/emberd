package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeProc lays out a minimal /proc fixture tree under a temp dir: for each
// pid it writes comm, cmdline (NUL-separated args), and an fd/ dir with the
// requested number of entries. It returns the tree root, usable as procRoot.
func fakeProc(t *testing.T, procs []fakeProcSpec) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		writeFile(t, filepath.Join(dir, "comm"), p.comm+"\n")
		if p.cmdline != "" {
			// /proc/<pid>/cmdline is NUL-separated; the fixture's space-joined
			// args are re-joined with NUL to mimic the real file.
			var raw []byte
			for i, a := range splitArgs(p.cmdline) {
				if i > 0 {
					raw = append(raw, 0)
				}
				raw = append(raw, []byte(a)...)
			}
			raw = append(raw, 0)
			if err := os.WriteFile(filepath.Join(dir, "cmdline"), raw, 0o644); err != nil {
				t.Fatalf("write cmdline: %v", err)
			}
		}
		fdDir := filepath.Join(dir, "fd")
		if err := os.MkdirAll(fdDir, 0o755); err != nil {
			t.Fatalf("mkdir fd: %v", err)
		}
		for i := 0; i < p.fds; i++ {
			writeFile(t, filepath.Join(fdDir, strconv.Itoa(i)), "")
		}
	}
	return root
}

type fakeProcSpec struct {
	pid     int
	comm    string
	cmdline string
	fds     int
}

// splitArgs splits a space-joined fixture cmdline into args.
func splitArgs(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestFindAndCountMatchingProcs(t *testing.T) {
	root := fakeProc(t, []fakeProcSpec{
		{pid: 10, comm: "firecracker"},
		{pid: 11, comm: "firecracker"},
		{pid: 12, comm: "emberd"},
		{pid: 13, comm: "bash"},
		{pid: 14, comm: "firecrackerd"}, // must NOT match (exact-name semantics)
	})
	// A stray non-pid directory must be skipped, not error the walk.
	if err := os.MkdirAll(filepath.Join(root, "acpi"), 0o755); err != nil {
		t.Fatal(err)
	}

	fcPids, err := findMatchingProcs(root, "firecracker")
	if err != nil {
		t.Fatalf("findMatchingProcs: %v", err)
	}
	if len(fcPids) != 2 || fcPids[0] != 10 || fcPids[1] != 11 {
		t.Errorf("firecracker pids = %v, want [10 11]", fcPids)
	}

	n, err := countMatchingProcs(root, "firecracker")
	if err != nil || n != 2 {
		t.Errorf("countMatchingProcs(firecracker) = %d, %v, want 2, nil", n, err)
	}

	ember, err := findMatchingProcs(root, "emberd")
	if err != nil || len(ember) != 1 || ember[0] != 12 {
		t.Errorf("emberd pids = %v, %v, want [12], nil", ember, err)
	}

	none, err := countMatchingProcs(root, "nonexistent")
	if err != nil || none != 0 {
		t.Errorf("countMatchingProcs(nonexistent) = %d, %v, want 0, nil", none, err)
	}
}

func TestCountFDs(t *testing.T) {
	root := fakeProc(t, []fakeProcSpec{{pid: 20, comm: "emberd", fds: 7}})
	n, err := countFDs(root, 20)
	if err != nil || n != 7 {
		t.Errorf("countFDs = %d, %v, want 7, nil", n, err)
	}
	if _, err := countFDs(root, 999); err == nil {
		t.Errorf("countFDs(missing pid) = nil error, want error")
	}
}

func TestAttributeFirecrackerPID(t *testing.T) {
	root := fakeProc(t, []fakeProcSpec{
		{pid: 30, comm: "firecracker", cmdline: "firecracker --api-sock /tmp/emberd/sb_aaaa/fc.sock --id sb-aaaa"},
		{pid: 31, comm: "firecracker", cmdline: "firecracker --api-sock /tmp/emberd/sb_bbbb/fc.sock --id sb-bbbb"},
		{pid: 32, comm: "emberd", cmdline: "emberd --addr 127.0.0.1:7777"},
	})

	pid, err := attributeFirecrackerPID(root, "sb_bbbb")
	if err != nil || pid != 31 {
		t.Errorf("attribute(sb_bbbb) = %d, %v, want 31, nil", pid, err)
	}

	// No match at all is an error, not a silent zero.
	if _, err := attributeFirecrackerPID(root, "sb_zzzz"); err == nil {
		t.Errorf("attribute(sb_zzzz) = nil error, want no-match error")
	}
}

func TestAttributeFirecrackerPIDAmbiguous(t *testing.T) {
	// Two firecracker processes whose cmdlines both contain the id => the
	// warm-pool ambiguity the memory scenario must fail loudly on.
	root := fakeProc(t, []fakeProcSpec{
		{pid: 40, comm: "firecracker", cmdline: "firecracker --api-sock /tmp/emberd/sb_dup/fc.sock"},
		{pid: 41, comm: "firecracker", cmdline: "firecracker --api-sock /tmp/emberd/sb_dup/other.sock"},
	})
	if _, err := attributeFirecrackerPID(root, "sb_dup"); err == nil {
		t.Errorf("attribute(ambiguous) = nil error, want ambiguity error")
	}
}

func TestParsePSSKiB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smaps_rollup")
	// A realistic smaps_rollup: the Pss: line must be picked, and the
	// Pss_Anon:/Pss_File: lines must NOT be mistaken for it.
	writeFile(t, path, "55d0c0000000-7ffd00000000 ---p 00000000 00:00 0 [rollup]\n"+
		"Rss:              123456 kB\n"+
		"Pss:               45678 kB\n"+
		"Pss_Anon:          40000 kB\n"+
		"Pss_File:           5678 kB\n"+
		"Shared_Clean:       1000 kB\n")

	kib, err := parsePSSKiB(path)
	if err != nil || kib != 45678 {
		t.Errorf("parsePSSKiB = %d, %v, want 45678, nil", kib, err)
	}

	mib, err := readPSSMiB(path)
	if err != nil {
		t.Fatalf("readPSSMiB: %v", err)
	}
	if want := 45678.0 / 1024.0; mib != want {
		t.Errorf("readPSSMiB = %v, want %v", mib, want)
	}
}

func TestParsePSSKiBUnreadableAndMalformed(t *testing.T) {
	dir := t.TempDir()

	// Unreadable (missing) file => error, never a zero.
	if _, err := parsePSSKiB(filepath.Join(dir, "missing")); err == nil {
		t.Errorf("parsePSSKiB(missing) = nil error, want error")
	}

	// A file with no Pss line => error.
	noPss := filepath.Join(dir, "no_pss")
	writeFile(t, noPss, "Rss:  100 kB\nShared_Clean: 4 kB\n")
	if _, err := parsePSSKiB(noPss); err == nil {
		t.Errorf("parsePSSKiB(no Pss) = nil error, want error")
	}

	// A malformed Pss line => error.
	bad := filepath.Join(dir, "bad")
	writeFile(t, bad, "Pss:  notanumber kB\n")
	if _, err := parsePSSKiB(bad); err == nil {
		t.Errorf("parsePSSKiB(malformed) = nil error, want error")
	}
}

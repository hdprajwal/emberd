package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Default paths for host environment facts. Exposed as vars (not just
// constants baked into the readers) so the readers themselves stay pure
// functions of a path, testable against fixture files.
const (
	cpuInfoPath   = "/proc/cpuinfo"
	osReleasePath = "/proc/sys/kernel/osrelease"
	governorPath  = "/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"
)

// Provenance records when and from what state a bench run was produced, so
// results can be compared across runs (bench-v2-spec.md §9.1).
type Provenance struct {
	// Timestamp is RFC 3339, UTC, captured at run start.
	Timestamp string `json:"timestamp"`
	// GitCommit is `git rev-parse --short HEAD` with a "-dirty" suffix when
	// the tree has uncommitted changes, or "unknown" if git is unavailable.
	GitCommit string `json:"git_commit"`
	// BenchFlags is the resolved flag set as a flat JSON object.
	BenchFlags map[string]any `json:"bench_flags"`
}

// Env records host and daemon environment facts. Guest-side fields
// (GuestRAMMiB, GuestVCPUs, BootPath) come from the daemon's GET /info and
// are never hardcoded — see CaptureEnv.
type Env struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	CPUModel        string `json:"cpu_model"`
	CPUCores        int    `json:"cpu_cores"`
	HostKernel      string `json:"host_kernel"`
	CPUGovernor     string `json:"cpu_governor"`
	GovernorWarning bool   `json:"governor_warning,omitempty"`
	FirecrackerVer  string `json:"firecracker_version"`
	// GuestRAMMiB and GuestVCPUs hold an int when the daemon's /info is
	// available, otherwise an explanatory "unknown (...)" string.
	GuestRAMMiB      any    `json:"guest_ram_mib"`
	GuestVCPUs       any    `json:"guest_vcpus"`
	BootPath         string `json:"boot_path"`
	DaemonInfoSource string `json:"daemon_info_source"`
}

// commandRunner abstracts external process execution so provenance capture
// can be tested against fixtures instead of a real git binary.
type commandRunner interface {
	// Run executes name with args in dir and returns trimmed stdout.
	Run(dir, name string, args ...string) (stdout string, err error)
}

// execRunner is the production commandRunner: it shells out for real.
type execRunner struct{}

func (execRunner) Run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// gitCommit resolves the short commit hash (with a "-dirty" suffix when the
// working tree has uncommitted changes) by running git in repoRoot via r.
// Returns "unknown" if git is unavailable or the repo root isn't a git
// checkout — the field is never omitted, only ever "unknown".
func gitCommit(r commandRunner, repoRoot string) string {
	short, err := r.Run(repoRoot, "git", "rev-parse", "--short", "HEAD")
	if err != nil || short == "" {
		return "unknown"
	}
	if status, err := r.Run(repoRoot, "git", "status", "--porcelain"); err == nil && status != "" {
		short += "-dirty"
	}
	return short
}

// CaptureProvenance builds the Provenance block for a run starting at now,
// using r to invoke git in repoRoot.
func CaptureProvenance(r commandRunner, repoRoot string, now time.Time, flags map[string]any) Provenance {
	return Provenance{
		Timestamp:  now.UTC().Format(time.RFC3339),
		GitCommit:  gitCommit(r, repoRoot),
		BenchFlags: flags,
	}
}

// readCPUModel extracts "model name" from a /proc/cpuinfo-formatted file at
// path, or "unknown" if it can't be read or parsed.
func readCPUModel(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("model name")) {
			parts := bytes.SplitN(line, []byte(":"), 2)
			if len(parts) == 2 {
				return string(bytes.TrimSpace(parts[1]))
			}
		}
	}
	return "unknown"
}

// readHostKernel reads a trimmed osrelease-formatted file at path, or
// "unknown" if it can't be read.
func readHostKernel(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// readCPUGovernor reads the scaling_governor file at path. warn is true
// whenever the governor is anything other than exactly "performance" —
// including when the file is missing (governor "unknown") — because in
// either case results may not be reproducible (bench-v2-spec.md §9.2).
func readCPUGovernor(path string) (governor string, warn bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown", true
	}
	governor = strings.TrimSpace(string(data))
	return governor, governor != "performance"
}

// firecrackerVersion runs `firecracker --version`, preferring PATH but
// falling back to the conventional ~/.local/bin install location.
func firecrackerVersion() string {
	bin := os.ExpandEnv("$HOME/.local/bin/firecracker")
	if p, err := exec.LookPath("firecracker"); err == nil {
		bin = p
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "unknown"
	}
	line := string(bytes.SplitN(bytes.TrimSpace(out), []byte("\n"), 2)[0])
	return line
}

// CaptureEnv gathers host and daemon environment facts. Guest facts (RAM,
// vCPUs, boot path) come from the daemon's GET /info; on a Phase 1 daemon
// (no /info route, 404) or any request failure they are recorded as an
// explicit "unknown (...)" string — never hardcoded (bench-v2-spec.md §9.2).
func CaptureEnv(ctx context.Context, c *Client) Env {
	governor, warn := readCPUGovernor(governorPath)
	if warn {
		fmt.Fprintf(os.Stderr, "warning: cpu governor is %q, not \"performance\" — results can swing 20-30%%\n", governor)
	}

	env := Env{
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		CPUModel:        readCPUModel(cpuInfoPath),
		CPUCores:        runtime.NumCPU(),
		HostKernel:      readHostKernel(osReleasePath),
		CPUGovernor:     governor,
		GovernorWarning: warn,
		FirecrackerVer:  firecrackerVersion(),
	}

	info, ok, err := c.Info(ctx)
	switch {
	case err != nil:
		unknown := fmt.Sprintf("unknown (error fetching /info: %v)", err)
		env.GuestRAMMiB, env.GuestVCPUs, env.BootPath = unknown, unknown, unknown
		env.DaemonInfoSource = unknown
	case !ok:
		unknown := "unknown (daemon has no /info endpoint)"
		env.GuestRAMMiB, env.GuestVCPUs, env.BootPath = unknown, unknown, unknown
		env.DaemonInfoSource = unknown
	default:
		env.GuestRAMMiB = info.GuestRAMMiB
		env.GuestVCPUs = info.GuestVCPUs
		env.BootPath = info.BootPath
		env.DaemonInfoSource = "GET /info"
	}
	return env
}

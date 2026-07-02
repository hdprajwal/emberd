package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeRunner is a scripted commandRunner for hermetic tests: it never
// shells out for real.
type fakeRunner struct {
	// responses maps "name args..." (joined by spaces) to a canned
	// (stdout, err) pair.
	responses map[string]fakeResponse
}

type fakeResponse struct {
	stdout string
	err    error
}

func (f fakeRunner) Run(_ string, name string, args ...string) (string, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	r, ok := f.responses[key]
	if !ok {
		return "", errors.New("fakeRunner: unscripted command: " + key)
	}
	return r.stdout, r.err
}

func TestGitCommitUnavailable(t *testing.T) {
	r := fakeRunner{responses: map[string]fakeResponse{
		"git rev-parse --short HEAD": {err: errors.New("exec: \"git\": executable file not found in $PATH")},
	}}
	if got := gitCommit(r, "/repo"); got != "unknown" {
		t.Errorf("gitCommit() = %q, want %q", got, "unknown")
	}
}

func TestGitCommitEmptyOutput(t *testing.T) {
	// e.g. HEAD unresolved (no commits yet) — git may exit 0 with empty
	// stdout in some setups; treat that as "unknown" too.
	r := fakeRunner{responses: map[string]fakeResponse{
		"git rev-parse --short HEAD": {stdout: ""},
	}}
	if got := gitCommit(r, "/repo"); got != "unknown" {
		t.Errorf("gitCommit() = %q, want %q", got, "unknown")
	}
}

func TestGitCommitClean(t *testing.T) {
	r := fakeRunner{responses: map[string]fakeResponse{
		"git rev-parse --short HEAD": {stdout: "abc1234"},
		"git status --porcelain":     {stdout: ""},
	}}
	if got := gitCommit(r, "/repo"); got != "abc1234" {
		t.Errorf("gitCommit() = %q, want %q", got, "abc1234")
	}
}

func TestGitCommitDirty(t *testing.T) {
	r := fakeRunner{responses: map[string]fakeResponse{
		"git rev-parse --short HEAD": {stdout: "abc1234"},
		"git status --porcelain":     {stdout: " M bench/main.go\n"},
	}}
	if got := gitCommit(r, "/repo"); got != "abc1234-dirty" {
		t.Errorf("gitCommit() = %q, want %q", got, "abc1234-dirty")
	}
}

func TestCaptureProvenance(t *testing.T) {
	r := fakeRunner{responses: map[string]fakeResponse{
		"git rev-parse --short HEAD": {stdout: "abc1234"},
		"git status --porcelain":     {stdout: ""},
	}}
	now := time.Date(2026, 7, 1, 22, 15, 30, 0, time.UTC)
	flags := map[string]any{"addr": "127.0.0.1:7777"}

	p := CaptureProvenance(r, "/repo", now, flags)
	if p.Timestamp != "2026-07-01T22:15:30Z" {
		t.Errorf("Timestamp = %q, want %q", p.Timestamp, "2026-07-01T22:15:30Z")
	}
	if p.GitCommit != "abc1234" {
		t.Errorf("GitCommit = %q, want %q", p.GitCommit, "abc1234")
	}
	if p.BenchFlags["addr"] != "127.0.0.1:7777" {
		t.Errorf("BenchFlags[addr] = %v, want %q", p.BenchFlags["addr"], "127.0.0.1:7777")
	}
}

func TestReadCPUModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cpuinfo")
	writeFile(t, path, "processor\t: 0\nmodel name\t: Fake CPU 9000\ncache size\t: 512 KB\n")

	if got := readCPUModel(path); got != "Fake CPU 9000" {
		t.Errorf("readCPUModel() = %q, want %q", got, "Fake CPU 9000")
	}
}

func TestReadCPUModelMissing(t *testing.T) {
	if got := readCPUModel(filepath.Join(t.TempDir(), "missing")); got != "unknown" {
		t.Errorf("readCPUModel() = %q, want %q", got, "unknown")
	}
}

func TestReadHostKernel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "osrelease")
	writeFile(t, path, "6.9.1-fake\n")

	if got := readHostKernel(path); got != "6.9.1-fake" {
		t.Errorf("readHostKernel() = %q, want %q", got, "6.9.1-fake")
	}
}

func TestReadHostKernelMissing(t *testing.T) {
	if got := readHostKernel(filepath.Join(t.TempDir(), "missing")); got != "unknown" {
		t.Errorf("readHostKernel() = %q, want %q", got, "unknown")
	}
}

func TestReadCPUGovernor(t *testing.T) {
	dir := t.TempDir()

	perfPath := filepath.Join(dir, "performance")
	writeFile(t, perfPath, "performance\n")
	if gov, warn := readCPUGovernor(perfPath); gov != "performance" || warn {
		t.Errorf("readCPUGovernor(performance) = (%q, %v), want (%q, false)", gov, warn, "performance")
	}

	powersavePath := filepath.Join(dir, "powersave")
	writeFile(t, powersavePath, "powersave\n")
	if gov, warn := readCPUGovernor(powersavePath); gov != "powersave" || !warn {
		t.Errorf("readCPUGovernor(powersave) = (%q, %v), want (%q, true)", gov, warn, "powersave")
	}

	if gov, warn := readCPUGovernor(filepath.Join(dir, "missing")); gov != "unknown" || !warn {
		t.Errorf("readCPUGovernor(missing) = (%q, %v), want (%q, true)", gov, warn, "unknown")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%s): %v", path, err)
	}
}

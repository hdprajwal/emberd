package firecracker

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigDefaults pins the snapshot-related defaulting: SnapshotDir derives
// from WorkDir, PoolSize distinguishes the -1 "disabled" sentinel from the 0
// "use default (3)" case, and each built-in pack gets a warm-up snippet.
func TestConfigDefaults(t *testing.T) {
	workDir := t.TempDir()

	// SnapshotDir defaults to WorkDir/snapshots.
	c := Config{WorkDir: workDir}
	if err := c.withDefaults(); err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if want := filepath.Join(workDir, "snapshots"); c.SnapshotDir != want {
		t.Fatalf("SnapshotDir = %q, want %q", c.SnapshotDir, want)
	}

	// PoolSize 0 → 3 (default), -1 preserved (disabled sentinel).
	if c.PoolSize != 3 {
		t.Fatalf("PoolSize default = %d, want 3", c.PoolSize)
	}
	cDisabled := Config{WorkDir: workDir, PoolSize: -1}
	if err := cDisabled.withDefaults(); err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if cDisabled.PoolSize != -1 {
		t.Fatalf("PoolSize -1 = %d, want -1 preserved", cDisabled.PoolSize)
	}

	// Built-in packs get warm-up code: python → print(1), shell → true.
	if got := c.Packs["python"].WarmupCode; got != "print(1)" {
		t.Fatalf("python WarmupCode = %q, want %q", got, "print(1)")
	}
	if got := c.Packs["shell"].WarmupCode; got != "true" {
		t.Fatalf("shell WarmupCode = %q, want %q", got, "true")
	}
}

// TestArtifactHash checks the content-addressed hash: stable across calls, 12
// hex chars, and sensitive to any input — artifact file contents, the pack
// interpreter, and the kernel args template.
func TestArtifactHash(t *testing.T) {
	// newManager builds a Manager over temp artifact files whose contents and
	// config are caller-controlled, so the test can perturb one input at a time.
	newManager := func(t *testing.T, kernel, initrd, rootfs, interp, kernelArgs string) *Manager {
		t.Helper()
		dir := t.TempDir()
		write := func(name, content string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			return p
		}
		fcBin := write("firecracker", "#!/bin/true\n")
		cfg := Config{
			FirecrackerBin:  fcBin,
			KernelImagePath: write("kernel", kernel),
			InitrdPath:      write("initrd", initrd),
			KernelArgs:      kernelArgs,
			Packs:           map[string]Pack{"python": {RootfsPath: write("rootfs", rootfs), Interpreter: interp}},
			WorkDir:         dir,
			SkipWarmOnStart: true,
		}
		m, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return m
	}

	base := newManager(t, "kern", "init", "root", "python3", "console=ttyS0")
	h1, err := base.artifactHash("python")
	if err != nil {
		t.Fatalf("artifactHash: %v", err)
	}
	if len(h1) != 12 {
		t.Fatalf("hash length = %d, want 12 (%q)", len(h1), h1)
	}

	// Stable across repeated calls.
	h2, err := base.artifactHash("python")
	if err != nil {
		t.Fatalf("artifactHash (2nd): %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash not stable: %q vs %q", h1, h2)
	}

	// Each input change must move the hash.
	cases := []struct {
		name                                  string
		kernel, initrd, rootfs, interp, kargs string
	}{
		{"kernel", "KERN", "init", "root", "python3", "console=ttyS0"},
		{"initrd", "kern", "INIT", "root", "python3", "console=ttyS0"},
		{"rootfs", "kern", "init", "ROOT", "python3", "console=ttyS0"},
		{"interpreter", "kern", "init", "root", "python3.12", "console=ttyS0"},
		{"kernelargs", "kern", "init", "root", "python3", "console=ttyS0 quiet"},
	}
	for _, tc := range cases {
		m := newManager(t, tc.kernel, tc.initrd, tc.rootfs, tc.interp, tc.kargs)
		h, err := m.artifactHash("python")
		if err != nil {
			t.Fatalf("%s: artifactHash: %v", tc.name, err)
		}
		if h == h1 {
			t.Fatalf("%s change did not alter hash (%q)", tc.name, h)
		}
	}
}

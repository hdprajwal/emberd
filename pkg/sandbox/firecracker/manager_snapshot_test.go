//go:build integration

package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// newIntegrationManager builds a Manager over the real verification artifacts
// under ~/firecracker-verify with a single python pack, a fresh WorkDir, and a
// SnapshotDir inside it. PoolSize is -1 (pools arrive in a later task). mutate,
// if non-nil, tweaks the Config before New — e.g. to point several managers at a
// shared SnapshotDir or to perturb KernelArgs. Cleanup tears down any still-live
// VMs directly, since Close() does not exist yet.
func newIntegrationManager(t *testing.T, mutate func(*Config)) *Manager {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	verify := filepath.Join(home, "firecracker-verify")
	workDir := t.TempDir()
	cfg := Config{
		KernelImagePath: filepath.Join(verify, "vmlinux-6.1.155"),
		InitrdPath:      filepath.Join(verify, "emberd-initramfs.cpio"),
		Packs: map[string]Pack{
			"python": {RootfsPath: filepath.Join(verify, "ubuntu-24.04.squashfs"), Interpreter: "python3"},
		},
		WorkDir:     workDir,
		SnapshotDir: filepath.Join(workDir, "snapshots"),
		PoolSize:    -1,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		m.mu.Lock()
		ids := make([]string, 0, len(m.vms))
		for id := range m.vms {
			ids = append(ids, id)
		}
		m.mu.Unlock()
		for _, id := range ids {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = m.Delete(ctx, id)
			cancel()
		}
	})
	return m
}

// firecrackerProcsUnder counts running firecracker processes whose command line
// references dir — used to prove a template VM is fully torn down (no orphaned
// hypervisor) after snapshot creation.
func firecrackerProcsUnder(t *testing.T, dir string) int {
	t.Helper()
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	count := 0
	for _, p := range cmdlines {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.ReplaceAll(string(data), "\x00", " ")
		if strings.Contains(s, "firecracker") && strings.Contains(s, dir) {
			count++
		}
	}
	return count
}

// TestCreateSnapshot boots a warmed-up template on New (warm on start) and
// asserts a valid content-addressed snapshot lands on disk, no torn temp dir is
// left behind, and the template hypervisor is gone.
func TestCreateSnapshot(t *testing.T) {
	m := newIntegrationManager(t, nil)

	hash, err := m.artifactHash("python")
	if err != nil {
		t.Fatalf("artifactHash: %v", err)
	}
	packDir := filepath.Join(m.cfg.SnapshotDir, "python")
	snapDir := filepath.Join(packDir, hash)
	for _, name := range []string{"mem", "state"} {
		fi, err := os.Stat(filepath.Join(snapDir, name))
		if err != nil {
			t.Fatalf("snapshot file %s missing: %v", name, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("snapshot file %s is empty", name)
		}
	}

	// No leftover temp dirs from the atomic-rename write.
	entries, err := os.ReadDir(packDir)
	if err != nil {
		t.Fatalf("read pack dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp dir %s under %s", e.Name(), packDir)
		}
	}

	// The template VM must be fully torn down: zero firecracker processes under
	// this manager's WorkDir.
	if n := firecrackerProcsUnder(t, m.cfg.WorkDir); n != 0 {
		t.Fatalf("expected 0 firecracker processes after snapshot creation, found %d", n)
	}
}

// TestCreateSnapshotWarmupFailure proves a nonzero warm-up exit fails New with a
// clear error rather than publishing a snapshot of a broken template.
func TestCreateSnapshotWarmupFailure(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	verify := filepath.Join(home, "firecracker-verify")
	workDir := t.TempDir()
	cfg := Config{
		KernelImagePath: filepath.Join(verify, "vmlinux-6.1.155"),
		InitrdPath:      filepath.Join(verify, "emberd-initramfs.cpio"),
		Packs: map[string]Pack{
			"python": {
				RootfsPath:  filepath.Join(verify, "ubuntu-24.04.squashfs"),
				Interpreter: "python3",
				WarmupCode:  "import sys; sys.exit(3)",
			},
		},
		WorkDir:     workDir,
		SnapshotDir: filepath.Join(workDir, "snapshots"),
		PoolSize:    -1,
	}
	_, err = New(cfg)
	if err == nil {
		t.Fatalf("New should fail when warm-up exec exits nonzero")
	}
	if !strings.Contains(err.Error(), "warm-up") {
		t.Fatalf("error should mention warm-up, got: %v", err)
	}
	// No firecracker process should survive the failed build.
	if n := firecrackerProcsUnder(t, workDir); n != 0 {
		t.Fatalf("expected 0 firecracker processes after failed snapshot creation, found %d", n)
	}
}

// TestSnapshotReuseAndInvalidation covers the content-addressed lifecycle:
// (a) an unchanged restart reuses the snapshot without rebuilding; (b) a config
// change builds a new hash dir and deletes the stale one; (c) a pre-planted
// bogus hash dir is cleaned at startup.
func TestSnapshotReuseAndInvalidation(t *testing.T) {
	shared := t.TempDir()
	snapDir := filepath.Join(shared, "snapshots")

	// First manager builds the snapshot.
	m1 := newIntegrationManager(t, func(c *Config) { c.SnapshotDir = snapDir })
	hash, err := m1.artifactHash("python")
	if err != nil {
		t.Fatalf("artifactHash: %v", err)
	}
	stateFile := filepath.Join(snapDir, "python", hash, "state")
	fi1, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("first snapshot missing: %v", err)
	}

	// (a) Reuse: a second manager over the same SnapshotDir must return fast and
	// leave the existing snapshot file untouched (same inode + mtime).
	start := time.Now()
	newIntegrationManager(t, func(c *Config) { c.SnapshotDir = snapDir })
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("New reused snapshot too slowly (%s); likely rebuilt", d)
	}
	fi2, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("snapshot missing after reuse: %v", err)
	}
	if !os.SameFile(fi1, fi2) || !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatalf("snapshot was rebuilt on reuse (inode/mtime changed)")
	}

	// (b) Invalidation: mutate KernelArgs → new hash built, old hash removed.
	m3 := newIntegrationManager(t, func(c *Config) {
		c.SnapshotDir = snapDir
		c.KernelArgs = "console=ttyS0 reboot=k panic=1 pci=off emberd.cachebust=1"
	})
	newHash, err := m3.artifactHash("python")
	if err != nil {
		t.Fatalf("artifactHash (mutated): %v", err)
	}
	if newHash == hash {
		t.Fatalf("mutated KernelArgs did not change the hash")
	}
	if _, err := os.Stat(filepath.Join(snapDir, "python", newHash, "state")); err != nil {
		t.Fatalf("new snapshot missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapDir, "python", hash)); !os.IsNotExist(err) {
		t.Fatalf("stale snapshot dir %s was not removed", hash)
	}

	// (c) Pre-plant a bogus hash dir alongside the valid one; a new manager over
	// the same (mutated) config must clean it while keeping the valid snapshot.
	bogus := filepath.Join(snapDir, "python", "deadbeefdead")
	if err := os.MkdirAll(bogus, 0o700); err != nil {
		t.Fatalf("plant bogus dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bogus, "mem"), []byte("junk"), 0o600); err != nil {
		t.Fatalf("plant bogus file: %v", err)
	}
	newIntegrationManager(t, func(c *Config) {
		c.SnapshotDir = snapDir
		c.KernelArgs = "console=ttyS0 reboot=k panic=1 pci=off emberd.cachebust=1"
	})
	if _, err := os.Stat(bogus); !os.IsNotExist(err) {
		t.Fatalf("bogus snapshot dir was not removed")
	}
	if _, err := os.Stat(filepath.Join(snapDir, "python", newHash, "state")); err != nil {
		t.Fatalf("valid snapshot removed by cleanup: %v", err)
	}
}

// registerVM inserts a restored/booted VM into m.vms so Exec/Delete find it,
// mirroring what Create does after coldBoot. Restore returns an unregistered vm
// (the pool/refiller own registration later), so the in-package tests do it here.
func registerVM(m *Manager, v *vm) {
	m.mu.Lock()
	m.vms[v.sb.ID] = v
	m.mu.Unlock()
}

// TestRestoreExec restores a single VM from the pack's template snapshot, runs
// an exec through the standard Exec path, and tears it down through the
// unchanged Delete. It also asserts the restore call itself is well under the
// cold-boot cost (< 200 ms), proving it took the load-snapshot path rather than
// falling back to a full boot.
func TestRestoreExec(t *testing.T) {
	m := newIntegrationManager(t, nil)
	ctx := context.Background()

	start := time.Now()
	v, err := m.restoreFromSnapshot(ctx, "python")
	if err != nil {
		t.Fatalf("restoreFromSnapshot: %v", err)
	}
	restoreLatency := time.Since(start)
	registerVM(m, v)
	t.Logf("restore latency: %s", restoreLatency)

	if restoreLatency >= 200*time.Millisecond {
		t.Fatalf("restore took %s, want < 200ms (should be the fast load-snapshot path, not a cold boot)", restoreLatency)
	}

	res, err := m.Exec(ctx, v.sb.ID, proto.ExecRequest{Code: "print(42)"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "42\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "42\n")
	}

	if err := m.Delete(ctx, v.sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestRestoreIsolation restores two VMs from the same snapshot concurrently and
// proves they are fully isolated: each writes a distinct file into its own guest
// /tmp and reads it back, seeing only its own value, and the two host vsock
// sockets live at distinct paths (distinct per-VM cwds).
func TestRestoreIsolation(t *testing.T) {
	m := newIntegrationManager(t, nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	vms := make([]*vm, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := m.restoreFromSnapshot(ctx, "python")
			if err != nil {
				errs[i] = err
				return
			}
			registerVM(m, v)
			vms[i] = v
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
	}

	if vms[0].vsockUDS == vms[1].vsockUDS {
		t.Fatalf("sibling restores share a vsock path %q; they must be distinct", vms[0].vsockUDS)
	}

	// Each VM writes a distinct marker into its own guest /tmp and reads it back.
	// Cross-talk (shared guest filesystem or shared socket) would surface as one
	// VM seeing the other's value.
	for i, v := range vms {
		want := fmt.Sprintf("vm-%d-marker", i)
		code := fmt.Sprintf(
			"open('/tmp/marker','w').write('%s'); print(open('/tmp/marker').read())",
			want,
		)
		res, err := m.Exec(ctx, v.sb.ID, proto.ExecRequest{Code: code})
		if err != nil {
			t.Fatalf("Exec vm %d: %v", i, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("vm %d exit code = %d, stderr=%q", i, res.ExitCode, res.Stderr)
		}
		if res.Stdout != want+"\n" {
			t.Fatalf("vm %d stdout = %q, want %q", i, res.Stdout, want+"\n")
		}
	}

	for _, v := range vms {
		if err := m.Delete(ctx, v.sb.ID); err != nil {
			t.Fatalf("Delete %s: %v", v.sb.ID, err)
		}
	}
}

// TestColdBootExec exercises the real cold-boot path end to end: it boots a
// python microVM, runs a trivial exec, and tears it down. It also asserts the
// guest vsock UDS lives at "v.sock" inside the sandbox's own work dir, proving
// the relative-path + per-VM-cwd design that snapshot restore depends on.
//
// Requires KVM and the verification artifacts under ~/firecracker-verify/.
// Setup is deliberately local and minimal here; the shared helper arrives in a
// later task.
func TestColdBootExec(t *testing.T) {
	workDir := t.TempDir()
	m, err := New(Config{WorkDir: workDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	sb, err := m.Create(ctx, "python")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		delCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Delete(delCtx, sb.ID)
	})

	// The guest vsock socket must bind to "v.sock" inside this sandbox's own
	// dir — a relative path baked into snapshot state, unique per VM via cwd.
	sockPath := filepath.Join(workDir, sb.ID, "v.sock")
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("expected vsock socket at %s while VM is live: %v", sockPath, err)
	}

	res, err := m.Exec(ctx, sb.ID, proto.ExecRequest{Code: "print(6*7)"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "42\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "42\n")
	}

	if err := m.Delete(ctx, sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

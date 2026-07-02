//go:build integration

package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// newIntegrationManager builds a Manager over the real verification artifacts
// under ~/firecracker-verify with a single python pack, a fresh WorkDir, and a
// SnapshotDir inside it. PoolSize is left at its default (3) so the warm pool is
// live; a test that needs pool-off behaviour sets PoolSize: -1 via mutate.
// mutate, if non-nil, tweaks the Config before New — e.g. to point several
// managers at a shared SnapshotDir or to perturb KernelArgs. Cleanup calls
// Close(), which drains the pool and deletes every still-live VM.
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
	}
	if mutate != nil {
		mutate(&cfg)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
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
	// Pool off: this test asserts zero firecracker processes survive snapshot
	// creation, which is a check on the template teardown. Warm pooled VMs would
	// be live firecracker processes under WorkDir and mask that signal.
	m := newIntegrationManager(t, func(c *Config) { c.PoolSize = -1 })

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
// proves they are fully isolated: both write distinct values to the SAME guest
// path (/tmp/marker), then each reads it back and must see only its own value —
// a shared filesystem would make the earlier writer read the later writer's
// value. The two host vsock sockets must also live at distinct paths (distinct
// per-VM cwds).
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

	// Write phase first, in BOTH VMs, to the SAME guest path; only then the read
	// phase. If the restores shared a filesystem (or one socket reached both),
	// vm 0's read — issued after vm 1's write — would see vm 1's value.
	marker := func(i int) string { return fmt.Sprintf("vm-%d-marker", i) }
	for i, v := range vms {
		code := fmt.Sprintf("open('/tmp/marker','w').write('%s')", marker(i))
		res, err := m.Exec(ctx, v.sb.ID, proto.ExecRequest{Code: code})
		if err != nil {
			t.Fatalf("write exec vm %d: %v", i, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("write vm %d exit code = %d, stderr=%q", i, res.ExitCode, res.Stderr)
		}
	}
	for i, v := range vms {
		want := marker(i)
		res, err := m.Exec(ctx, v.sb.ID, proto.ExecRequest{Code: "print(open('/tmp/marker').read())"})
		if err != nil {
			t.Fatalf("read exec vm %d: %v", i, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("read vm %d exit code = %d, stderr=%q", i, res.ExitCode, res.Stderr)
		}
		if res.Stdout != want+"\n" {
			t.Fatalf("vm %d read back %q, want %q (cross-talk between sibling restores)", i, res.Stdout, want+"\n")
		}
	}

	for _, v := range vms {
		if err := m.Delete(ctx, v.sb.ID); err != nil {
			t.Fatalf("Delete %s: %v", v.sb.ID, err)
		}
	}
}

// TestCreateUsesRestore proves Create() takes the fast restore path once a
// snapshot exists. The manager warms on start (default), so a template snapshot
// is already registered; three back-to-back Create() calls must each land well
// under the cold-boot cost (< 200 ms), each run real code, and each tear down
// cleanly.
func TestCreateUsesRestore(t *testing.T) {
	m := newIntegrationManager(t, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		start := time.Now()
		sb, err := m.Create(ctx, "python")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		latency := time.Since(start)
		t.Logf("create %d latency: %s", i, latency)
		if latency >= 200*time.Millisecond {
			t.Fatalf("Create %d took %s, want < 200ms (should restore from snapshot, not cold boot)", i, latency)
		}

		res, err := m.Exec(ctx, sb.ID, proto.ExecRequest{Code: "print(6*7)"})
		if err != nil {
			t.Fatalf("Exec %d: %v", i, err)
		}
		if res.ExitCode != 0 || res.Stdout != "42\n" {
			t.Fatalf("Create %d exec: exit=%d stdout=%q stderr=%q", i, res.ExitCode, res.Stdout, res.Stderr)
		}

		if err := m.Delete(ctx, sb.ID); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
}

// TestRestoreFallbackToColdBoot corrupts the registered snapshot (truncates its
// memory image to zero bytes) and asserts Create() still succeeds: acquire must
// log the restore failure and fall back to a cold boot rather than surfacing the
// error. The > 300 ms latency proves the slow path actually ran, and the exec
// proves the fallback VM is fully functional.
func TestRestoreFallbackToColdBoot(t *testing.T) {
	// Pool off: a live pool would pre-restore VMs at New() (while the snapshot is
	// still valid), so Create would pop a healthy pooled VM instead of taking the
	// restore path this test corrupts. -1 forces Create straight to restore →
	// cold-boot fallback.
	m := newIntegrationManager(t, func(c *Config) { c.PoolSize = -1 })
	ctx := context.Background()

	m.snapshotMu.RLock()
	snap, ok := m.snapshots["python"]
	m.snapshotMu.RUnlock()
	if !ok {
		t.Fatalf("expected a registered snapshot after warm-on-start New")
	}
	if err := os.Truncate(snap.memFile, 0); err != nil {
		t.Fatalf("truncate snapshot mem file: %v", err)
	}

	start := time.Now()
	sb, err := m.Create(ctx, "python")
	if err != nil {
		t.Fatalf("Create must still succeed via cold-boot fallback: %v", err)
	}
	latency := time.Since(start)
	t.Logf("fallback create latency: %s", latency)
	if latency <= 300*time.Millisecond {
		t.Fatalf("Create took %s, want > 300ms (a corrupt snapshot should force a cold boot)", latency)
	}

	res, err := m.Exec(ctx, sb.ID, proto.ExecRequest{Code: "print(6*7)"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "42\n" {
		t.Fatalf("fallback exec: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}

	if err := m.Delete(ctx, sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestLazySnapshotBuild proves a daemon started with SkipWarmOnStart self-heals
// to the fast path. With no snapshot on disk the first Create cold boots (slow)
// but succeeds and kicks off a one-time background build; once that build
// publishes and registers the snapshot, a subsequent Create restores fast.
func TestLazySnapshotBuild(t *testing.T) {
	m := newIntegrationManager(t, func(c *Config) { c.SkipWarmOnStart = true })
	ctx := context.Background()

	// First Create: no snapshot yet, so it must cold boot (slow) and succeed.
	start := time.Now()
	sb1, err := m.Create(ctx, "python")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	firstLatency := time.Since(start)
	t.Logf("first (cold) create latency: %s", firstLatency)
	if firstLatency <= 300*time.Millisecond {
		t.Fatalf("first Create took %s, expected a cold boot > 300ms", firstLatency)
	}

	hash, err := m.artifactHash("python")
	if err != nil {
		t.Fatalf("artifactHash: %v", err)
	}
	snapDir := filepath.Join(m.cfg.SnapshotDir, "python", hash)

	// Poll for the background build to both land on disk and register in
	// m.snapshots (the signal Create actually reads).
	registered := func() bool {
		m.snapshotMu.RLock()
		_, ok := m.snapshots["python"]
		m.snapshotMu.RUnlock()
		return ok
	}
	deadline := time.Now().Add(30 * time.Second)
	for !(snapshotComplete(snapDir) && registered()) {
		if time.Now().After(deadline) {
			t.Fatalf("lazy snapshot for python did not appear/register within 30s (dir %s)", snapDir)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Second Create now takes the fast restore path.
	start = time.Now()
	sb2, err := m.Create(ctx, "python")
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	secondLatency := time.Since(start)
	t.Logf("second (restore) create latency: %s", secondLatency)
	if secondLatency >= 200*time.Millisecond {
		t.Fatalf("second Create took %s, want < 200ms (should restore from the lazily built snapshot)", secondLatency)
	}

	if err := m.Delete(ctx, sb1.ID); err != nil {
		t.Fatalf("Delete sb1: %v", err)
	}
	if err := m.Delete(ctx, sb2.ID); err != nil {
		t.Fatalf("Delete sb2: %v", err)
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
	// Pool off: this test exercises the boot + vsock path directly and never calls
	// Close(), so disabling the pool avoids pre-warming (and leaking) idle VMs and
	// a refiller goroutine.
	m, err := New(Config{WorkDir: workDir, PoolSize: -1})
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

// TestPoolHit proves steady-state Create() is a pool pop, not a restore. With
// PoolSize 2 and warm-on-start the pool holds two pre-restored VMs; two
// consecutive Creates must each return in well under 50 ms (a channel receive,
// far below even the ~20 ms restore) and each VM must actually run code.
func TestPoolHit(t *testing.T) {
	m := newIntegrationManager(t, func(c *Config) { c.PoolSize = 2 })
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		start := time.Now()
		sb, err := m.Create(ctx, "python")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		latency := time.Since(start)
		t.Logf("pool-hit create %d latency: %s", i, latency)
		if latency >= 50*time.Millisecond {
			t.Fatalf("Create %d took %s, want < 50ms (should be a pool pop)", i, latency)
		}

		res, err := m.Exec(ctx, sb.ID, proto.ExecRequest{Code: "print(6*7)"})
		if err != nil {
			t.Fatalf("Exec %d: %v", i, err)
		}
		if res.ExitCode != 0 || res.Stdout != "42\n" {
			t.Fatalf("Create %d exec: exit=%d stdout=%q stderr=%q", i, res.ExitCode, res.Stdout, res.Stderr)
		}

		if err := m.Delete(ctx, sb.ID); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
}

// TestPoolRefill proves the background refiller replenishes a drained pool. Two
// Creates empty the PoolSize-2 pool and each signal refillCh; the pool must
// climb back to 2 within 30 s.
func TestPoolRefill(t *testing.T) {
	m := newIntegrationManager(t, func(c *Config) { c.PoolSize = 2 })
	ctx := context.Background()

	var ids []string
	for i := 0; i < 2; i++ {
		sb, err := m.Create(ctx, "python")
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		ids = append(ids, sb.ID)
	}

	// len() on a channel is a safe concurrent read against the refiller's pushes.
	pool := m.pools["python"]
	deadline := time.Now().Add(30 * time.Second)
	for len(pool) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("pool did not refill to 2 within 30s (len=%d)", len(pool))
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, id := range ids {
		if err := m.Delete(ctx, id); err != nil {
			t.Fatalf("Delete %s: %v", id, err)
		}
	}
}

// TestCloseDrainsPool proves Close() leaves no Firecracker process behind. It
// fills a PoolSize-2 pool, waits for the refiller to top it back up after one
// live Create, then Close()s and asserts the pool channel is empty and no
// firecracker process under this manager's WorkDir survives — covering both the
// idle pooled VMs and the one live sandbox Close tears down.
func TestCloseDrainsPool(t *testing.T) {
	m := newIntegrationManager(t, func(c *Config) { c.PoolSize = 2 })
	ctx := context.Background()

	// One live sandbox in addition to the warm pool. Its Create pops a pooled VM
	// and signals a refill.
	if _, err := m.Create(ctx, "python"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Wait for the refiller to bring the pool back to full so Close has a fully
	// populated pool to drain.
	pool := m.pools["python"]
	deadline := time.Now().Add(30 * time.Second)
	for len(pool) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("pool did not refill to 2 before Close (len=%d)", len(pool))
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := len(pool); n != 0 {
		t.Fatalf("pool not drained after Close: len=%d", n)
	}
	// Every Firecracker process for this manager — pooled and live — must be gone.
	if n := firecrackerProcsUnder(t, m.cfg.WorkDir); n != 0 {
		t.Fatalf("expected 0 firecracker processes after Close, found %d", n)
	}

	// Close is idempotent: a second call (and the deferred t.Cleanup) must no-op.
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestRestoreEntropyDiverges restores two VMs from ONE snapshot and asserts
// their RNGs diverge: each execs uuid.uuid4() and the two outputs must differ.
// Both wake from the same memory image, so without fresh entropy they would
// produce identical UUIDs. Firecracker's VMGenID device plus the guest kernel's
// vmgenid driver reseed the RNG on restore, which should make this pass with no
// guest-side work. If it fails (kernel lacks CONFIG_VMGENID), the spec's
// fallback seeds /dev/urandom from a host-supplied header instead.
func TestRestoreEntropyDiverges(t *testing.T) {
	m := newIntegrationManager(t, nil)
	ctx := context.Background()

	const code = "import uuid; print(uuid.uuid4())"
	outputs := make([]string, 2)
	for i := 0; i < 2; i++ {
		v, err := m.restoreFromSnapshot(ctx, "python")
		if err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
		registerVM(m, v)

		res, err := m.Exec(ctx, v.sb.ID, proto.ExecRequest{Code: code})
		if err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("exec %d exit code = %d, stderr=%q", i, res.ExitCode, res.Stderr)
		}
		outputs[i] = strings.TrimSpace(res.Stdout)
		t.Logf("restore %d uuid: %s", i, outputs[i])

		if err := m.Delete(ctx, v.sb.ID); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}

	if outputs[0] == "" {
		t.Fatalf("empty uuid output")
	}
	if outputs[0] == outputs[1] {
		t.Fatalf("two restores produced identical randomness %q; RNG did not reseed across restore (VMGenID/entropy hygiene broken)", outputs[0])
	}
}

// TestRestoreClockResync proves a restored guest self-heals its wall clock. The
// snapshot was taken earlier, so the restored VM wakes with a stale clock; the
// manager stamps HostTimeUnixNano on every exec and PID-1 emberd-init steps
// CLOCK_REALTIME to it. The guest's reported time.time() must land within 2 s of
// the host clock at exec time.
func TestRestoreClockResync(t *testing.T) {
	m := newIntegrationManager(t, nil)
	ctx := context.Background()

	v, err := m.restoreFromSnapshot(ctx, "python")
	if err != nil {
		t.Fatalf("restoreFromSnapshot: %v", err)
	}
	registerVM(m, v)

	hostBefore := time.Now()
	res, err := m.Exec(ctx, v.sb.ID, proto.ExecRequest{Code: "import time; print(time.time())"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	hostAfter := time.Now()
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}

	guestUnix, err := strconv.ParseFloat(strings.TrimSpace(res.Stdout), 64)
	if err != nil {
		t.Fatalf("parse guest time %q: %v", res.Stdout, err)
	}
	guestTime := time.Unix(0, int64(guestUnix*float64(time.Second)))
	t.Logf("host=%s guest=%s", hostAfter.Format(time.RFC3339Nano), guestTime.Format(time.RFC3339Nano))

	// The guest read happens between hostBefore and hostAfter; allow 2 s slack
	// on either side of that window.
	const tol = 2 * time.Second
	if guestTime.Before(hostBefore.Add(-tol)) || guestTime.After(hostAfter.Add(tol)) {
		t.Fatalf("guest clock %s outside [%s, %s] +/- %s of host (resync failed)",
			guestTime, hostBefore, hostAfter, tol)
	}

	if err := m.Delete(ctx, v.sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

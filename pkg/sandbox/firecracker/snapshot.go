package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	fc "github.com/firecracker-microvm/firecracker-go-sdk"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// artifactHash returns the first 12 hex chars of a sha256 over everything that
// makes a pack's template snapshot valid: the kernel image, the initramfs
// (which carries emberd-init), the pack's rootfs squashfs, the pack interpreter
// string, and the kernel args template. Any change to those must invalidate an
// existing snapshot, so all of them feed the hash. The digest is streamed file
// by file so hashing a ~400 MB squashfs never loads it fully into memory.
//
// The 12-char prefix is the directory name under SnapshotDir/<pack>/; a torn or
// stale snapshot lives under a different name and is cleaned at startup.
func (m *Manager) artifactHash(packName string) (string, error) {
	pack := m.cfg.Packs[packName]

	h := sha256.New()
	for _, p := range []string{m.cfg.KernelImagePath, m.cfg.InitrdPath, pack.RootfsPath} {
		if err := hashFile(h, p); err != nil {
			return "", err
		}
	}
	// String inputs are length-delimited so no concatenation of one input's
	// tail with the next can collide (e.g. interpreter "a"+args "b" vs "ab").
	fmt.Fprintf(h, "\x00interp=%d:%s", len(pack.Interpreter), pack.Interpreter)
	fmt.Fprintf(h, "\x00kargs=%d:%s", len(m.cfg.KernelArgs), m.cfg.KernelArgs)

	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// hashFile streams a file's contents into h.
func hashFile(h io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("hash artifact %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash artifact %s: %w", path, err)
	}
	return nil
}

// loadOrBuildSnapshots is New()'s per-pack snapshot bring-up. For each pack it:
//   - computes the content hash and registers an existing valid snapshot under
//     SnapshotDir/<pack>/<hash>/ if both mem and state are present and non-empty;
//   - deletes every other entry under SnapshotDir/<pack>/ — stale snapshots from
//     older artifacts and leftover .tmp-* dirs from a crashed write;
//   - builds the snapshot when none is valid and warm-on-start is enabled.
//
// A build failure is returned to fail New(); an unchanged restart registers the
// existing snapshot and boots nothing.
func (m *Manager) loadOrBuildSnapshots(ctx context.Context) error {
	for packName := range m.cfg.Packs {
		hash, err := m.artifactHash(packName)
		if err != nil {
			return err
		}
		packDir := filepath.Join(m.cfg.SnapshotDir, packName)
		validDir := filepath.Join(packDir, hash)

		valid := snapshotComplete(validDir)
		if valid {
			m.snapshotMu.Lock()
			m.snapshots[packName] = snapshotPaths{
				memFile: filepath.Join(validDir, "mem"),
				vmState: filepath.Join(validDir, "state"),
			}
			m.snapshotMu.Unlock()
		}

		// Everything under packDir that is not the valid hash dir is junk: a
		// snapshot for different artifacts, or a torn temp dir. If nothing is
		// valid, every entry here is junk.
		entries, err := os.ReadDir(packDir)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("scan snapshot dir %s: %w", packDir, err)
		}
		for _, e := range entries {
			if valid && e.Name() == hash {
				continue
			}
			_ = os.RemoveAll(filepath.Join(packDir, e.Name()))
		}

		if !valid && !m.cfg.SkipWarmOnStart {
			if err := m.createSnapshot(ctx, packName); err != nil {
				return err
			}
		}
	}
	return nil
}

// snapshotComplete reports whether dir holds a non-torn snapshot: both mem and
// state exist and are non-empty. The atomic rename in createSnapshot guarantees
// a dir under the final hash name is only ever fully written, but the size check
// is a cheap belt-and-braces guard against a pre-existing bad dir.
func snapshotComplete(dir string) bool {
	for _, name := range []string{"mem", "state"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// createSnapshot boots a template VM for packName, warms up its interpreter,
// pauses it, and snapshots it into SnapshotDir/<pack>/<hash>/. The snapshot is
// written to a temp dir first and atomically renamed into place so a crash
// mid-write never leaves a half snapshot that the startup check would accept.
// The template is fully torn down before the snapshot is published, so no
// hypervisor process outlives the build. On success the paths are registered in
// m.snapshots.
func (m *Manager) createSnapshot(ctx context.Context, packName string) error {
	pack := m.cfg.Packs[packName]
	hash, err := m.artifactHash(packName)
	if err != nil {
		return err
	}

	v, err := m.coldBoot(ctx, packName)
	if err != nil {
		return fmt.Errorf("create snapshot %s: cold boot template: %w", packName, err)
	}

	// The template is never registered in m.vms, so tear it down directly.
	// Idempotent enough for the single call each path makes.
	teardown := func() {
		_ = v.machine.StopVMM()
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = v.machine.Wait(waitCtx)
		cancel()
		v.cancel()
		_ = os.RemoveAll(v.dir)
	}

	// Warm-up exec: pages the interpreter in so restored clones start warm. The
	// exec closes its vsock connection when it returns, so none is open when we
	// pause below. Any nonzero exit (or agent error) fails the build.
	res, err := m.execVM(ctx, v, proto.ExecRequest{Code: pack.WarmupCode})
	if err != nil {
		teardown()
		return fmt.Errorf("create snapshot %s: warm-up exec: %w", packName, err)
	}
	if res.Error != "" {
		teardown()
		return fmt.Errorf("create snapshot %s: warm-up exec agent error: %s", packName, res.Error)
	}
	if res.ExitCode != 0 {
		teardown()
		return fmt.Errorf("create snapshot %s: warm-up exec exited %d: %s", packName, res.ExitCode, res.Stderr)
	}

	if err := v.machine.PauseVM(ctx); err != nil {
		teardown()
		return fmt.Errorf("create snapshot %s: pause vm: %w", packName, err)
	}

	packDir := filepath.Join(m.cfg.SnapshotDir, packName)
	if err := os.MkdirAll(packDir, 0o700); err != nil {
		teardown()
		return fmt.Errorf("create snapshot %s: mkdir pack dir: %w", packName, err)
	}
	tmpDir, err := os.MkdirTemp(packDir, ".tmp-")
	if err != nil {
		teardown()
		return fmt.Errorf("create snapshot %s: temp dir: %w", packName, err)
	}
	memFile := filepath.Join(tmpDir, "mem")
	vmState := filepath.Join(tmpDir, "state")
	if err := v.machine.CreateSnapshot(ctx, memFile, vmState); err != nil {
		_ = os.RemoveAll(tmpDir)
		teardown()
		return fmt.Errorf("create snapshot %s: %w", packName, err)
	}

	// The template served its purpose; stop it before publishing so no firecracker
	// process outlives the build.
	teardown()

	finalDir := filepath.Join(packDir, hash)
	_ = os.RemoveAll(finalDir) // defensive: clear any pre-existing dir at this name
	if err := os.Rename(tmpDir, finalDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("create snapshot %s: publish: %w", packName, err)
	}

	m.snapshotMu.Lock()
	m.snapshots[packName] = snapshotPaths{
		memFile: filepath.Join(finalDir, "mem"),
		vmState: filepath.Join(finalDir, "state"),
	}
	m.snapshotMu.Unlock()
	return nil
}

// acquire returns a live, unregistered *vm for packName by the fastest path
// available, per the fast-boot spec's "Create / acquire" flow:
//
//   - Pool pop: if a pool exists for the pack, a non-blocking receive claims a
//     pre-warmed VM. The pool and its refiller land in a later task, so m.pools
//     is nil here and this is a guarded no-op (a receive on a nil channel blocks
//     forever, hence the nil guard).
//   - Snapshot restore: if a template snapshot is registered, restore from it.
//     A restore failure must never fail a Create it can route around, so it logs
//     and falls back to a cold boot rather than surfacing the error.
//   - Cold boot + lazy build: with no snapshot yet, cold boot now and kick off a
//     one-time background snapshot build so subsequent creates take the fast
//     restore path. A daemon started with SkipWarmOnStart therefore self-heals.
//
// The caller (Create) owns registration in m.vms.
func (m *Manager) acquire(ctx context.Context, packName string) (*vm, error) {
	// Pool pop — no-op until the pool lands. Nil-guard the channel: a receive on
	// a nil channel blocks forever, so this must never touch a nil pool.
	if pool := m.pools[packName]; pool != nil {
		select {
		case v := <-pool:
			if v != nil {
				return v, nil
			}
		default:
		}
	}

	m.snapshotMu.RLock()
	_, haveSnapshot := m.snapshots[packName]
	m.snapshotMu.RUnlock()

	if haveSnapshot {
		v, err := m.restoreFromSnapshot(ctx, packName)
		if err == nil {
			return v, nil
		}
		// Restore is best-effort: a corrupt or unreadable snapshot must not fail
		// Create when a cold boot can still serve the request. Log and fall back.
		m.logger.WithField("pack", packName).WithError(err).
			Warn("snapshot restore failed; falling back to cold boot")
		return m.coldBoot(ctx, packName)
	}

	// No snapshot registered: serve this create with a cold boot and build the
	// template in the background so the next create is fast.
	v, err := m.coldBoot(ctx, packName)
	if err != nil {
		return nil, err
	}
	m.buildSnapshotOnce(packName)
	return v, nil
}

// buildSnapshotOnce kicks off createSnapshot for packName in the background
// exactly once per pack, even under concurrent cold boots. A per-pack sync.Once
// (fetched or created under buildMu) serializes the racers so only one goroutine
// builds the template. A lazy build that fails is logged and its Once dropped so
// a later cold boot retries — creates keep cold-booting until a build succeeds.
func (m *Manager) buildSnapshotOnce(packName string) {
	m.buildMu.Lock()
	once, ok := m.buildOnce[packName]
	if !ok {
		once = &sync.Once{}
		m.buildOnce[packName] = once
	}
	m.buildMu.Unlock()

	go once.Do(func() {
		// A background build outlives the triggering request, so it gets its own
		// context rather than the caller's (which may be cancelled by then).
		if err := m.createSnapshot(context.Background(), packName); err != nil {
			m.logger.WithField("pack", packName).WithError(err).
				Warn("lazy snapshot build failed; creates keep cold-booting")
			// Drop the spent Once so the next cold boot retries the build.
			m.buildMu.Lock()
			delete(m.buildOnce, packName)
			m.buildMu.Unlock()
		}
	})
}

// restoreFromSnapshot loads packName's registered template snapshot into a fresh
// microVM and returns its live handle, shaped identically to a cold-booted vm so
// Exec and Delete need no special-casing. The returned vm is NOT registered in
// m.vms — the caller (Create's acquire path, or a refiller) owns registration.
//
// Restore is the fast path: 15–30 ms vs ~400 ms for a cold boot, because the
// guest's page cache and the listening emberd-init both survive in the memory
// image, so waitReady converges in a probe or two instead of waiting out a full
// kernel + userspace boot.
//
// Per the fast-boot spec's correction #2 the restore fc.Config carries only
// SocketPath and VMID (both filled in by launchVM): fc.WithSnapshot swaps in
// the load-snapshot handler list, and the snapshot state supplies the drives,
// the vsock device, and the machine shape. The vsock device's path was baked in
// as the relative "v.sock", so the restored socket binds inside this VM's own
// dir (cmd.Dir), giving every clone a distinct host socket without an
// (impossible) post-restore vsock patch.
func (m *Manager) restoreFromSnapshot(ctx context.Context, packName string) (*vm, error) {
	m.snapshotMu.RLock()
	snap, ok := m.snapshots[packName]
	m.snapshotMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("restore %s: no snapshot registered", packName)
	}

	return m.launchVM(ctx, packName, func() (fc.Config, []fc.Opt) {
		// Zero fc.Config: the load-snapshot handler list (installed by
		// fc.WithSnapshot) restores drives, vsock, and machine shape from the
		// state file, so passing them here would be redundant. SocketPath is
		// per-VM and on the command line, never part of snapshot state.
		return fc.Config{}, []fc.Opt{
			fc.WithSnapshot(snap.memFile, snap.vmState, func(sc *fc.SnapshotConfig) {
				// Resume the guest as part of the load so it is immediately
				// runnable; the snapshot was taken paused, right after warm-up.
				sc.ResumeVM = true
			}),
		}
	})
}

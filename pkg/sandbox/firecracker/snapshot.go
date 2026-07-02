package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	fc "github.com/firecracker-microvm/firecracker-go-sdk"

	"github.com/hdprajwal/emberd/pkg/proto"
	"github.com/hdprajwal/emberd/pkg/sandbox"
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
// SocketPath and VMID: fc.WithSnapshot swaps in the load-snapshot handler list,
// and the snapshot state supplies the drives, the vsock device, and the machine
// shape. The vsock device's path was baked in as the relative "v.sock", so the
// restored socket binds inside this VM's own dir (cmd.Dir), giving every clone a
// distinct host socket without an (impossible) post-restore vsock patch.
func (m *Manager) restoreFromSnapshot(ctx context.Context, packName string) (*vm, error) {
	m.snapshotMu.RLock()
	snap, ok := m.snapshots[packName]
	m.snapshotMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("restore %s: no snapshot registered", packName)
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(m.cfg.WorkDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create sandbox dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	logFile, err := os.OpenFile(filepath.Join(dir, "vm.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create vm log: %w", err)
	}

	// The vsock UDS the host dials is dir/v.sock: the snapshot baked in the
	// relative "v.sock", and cmd.Dir below pins this Firecracker process to dir,
	// so the restored device rebinds there. Absolute join for host-side dialing.
	vsockUDS := filepath.Join(dir, "v.sock")

	// Only SocketPath and VMID — the load-snapshot handler list (installed by
	// fc.WithSnapshot) restores drives, vsock, and machine shape from the state
	// file, so passing them here would be redundant. SocketPath is per-VM and on
	// the command line, never part of snapshot state.
	socketPath := filepath.Join(dir, "fc.sock")
	fcID := strings.ReplaceAll(id, "_", "-")
	fcCfg := fc.Config{
		SocketPath: socketPath,
		VMID:       fcID,
	}

	// The microVM must outlive the restore request, so it gets its own context
	// cancelled only on Delete, exactly like coldBoot.
	vmCtx, cancel := context.WithCancel(context.Background())

	// cmd.Dir pins the process to dir so the restored relative "v.sock" resolves
	// to dir/v.sock — same mechanism coldBoot uses for its template.
	cmd := fc.VMCommandBuilder{}.
		WithBin(m.cfg.FirecrackerBin).
		WithSocketPath(socketPath).
		WithArgs([]string{"--id", fcID}).
		WithStdout(logFile).
		WithStderr(logFile).
		Build(vmCtx)
	cmd.Dir = dir

	machine, err := fc.NewMachine(vmCtx, fcCfg,
		fc.WithLogger(m.logger.WithField("sandbox", id)),
		fc.WithProcessRunner(cmd),
		fc.WithSnapshot(snap.memFile, snap.vmState, func(sc *fc.SnapshotConfig) {
			// Resume the guest as part of the load so it is immediately runnable;
			// the snapshot was taken paused, right after warm-up.
			sc.ResumeVM = true
		}),
	)
	if err != nil {
		cancel()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("new machine (restore): %w", err)
	}

	if err := machine.Start(vmCtx); err != nil {
		cancel()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("start machine (restore): %w", err)
	}

	// The guest's emberd-init was already listening when the snapshot was taken,
	// so this converges in a probe or two rather than waiting out a boot.
	if err := waitReady(ctx, vsockUDS, proto.GuestPort, m.cfg.BootTimeout); err != nil {
		_ = machine.StopVMM()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = machine.Wait(stopCtx)
		stopCancel()
		cancel()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("restore %s: %w", id, err)
	}

	return &vm{
		sb:        &sandbox.Sandbox{ID: id, LanguagePack: packName},
		machine:   machine,
		cancel:    cancel,
		dir:       dir,
		vsockUDS:  vsockUDS,
		vsockPort: proto.GuestPort,
	}, nil
}

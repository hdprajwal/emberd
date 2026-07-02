package firecracker

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// fakeGuest mimics Firecracker's host-side hybrid-vsock Unix socket. Its socket
// exists from the start (Firecracker creates it at VM-config time), but it only
// completes the "CONNECT"/"OK" handshake once ready is set — before that it
// resets the connection, exactly as Firecracker does while no guest port is
// listening. This lets a test model a still-booting microVM.
type fakeGuest struct {
	ln    net.Listener
	ready atomic.Bool
	wg    sync.WaitGroup
	stop  chan struct{}
}

func startFakeGuest(t *testing.T, udsPath string) *fakeGuest {
	t.Helper()
	ln, err := net.Listen("unix", udsPath)
	if err != nil {
		t.Fatalf("listen fake guest: %v", err)
	}
	g := &fakeGuest{ln: ln, stop: make(chan struct{})}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go g.serve(conn)
		}
	}()
	return g
}

func (g *fakeGuest) serve(c net.Conn) {
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		return
	}
	if !g.ready.Load() {
		return // reset: no guest port listening yet
	}
	_, _ = c.Write([]byte("OK 0\n"))
	<-g.stop // hold the conn open until shutdown
}

func (g *fakeGuest) Close() {
	close(g.stop)
	g.ln.Close()
	g.wg.Wait()
}

func TestWaitReadyAcceptsListeningGuest(t *testing.T) {
	uds := filepath.Join(t.TempDir(), "vsock.sock")
	g := startFakeGuest(t, uds)
	g.ready.Store(true)
	defer g.Close()

	if err := waitReady(context.Background(), uds, proto.GuestPort, 2*time.Second); err != nil {
		t.Fatalf("waitReady on a listening guest: %v", err)
	}
}

func TestWaitReadyWaitsForLateGuest(t *testing.T) {
	uds := filepath.Join(t.TempDir(), "vsock.sock")
	g := startFakeGuest(t, uds) // socket up, but not yet accepting on the port
	defer g.Close()

	// Flip to ready partway through, simulating emberd-init finishing bootstrap.
	timer := time.AfterFunc(150*time.Millisecond, func() { g.ready.Store(true) })
	defer timer.Stop()

	if err := waitReady(context.Background(), uds, proto.GuestPort, 3*time.Second); err != nil {
		t.Fatalf("waitReady should converge once the guest comes up: %v", err)
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	uds := filepath.Join(t.TempDir(), "vsock.sock") // nothing ever listens here
	start := time.Now()
	err := waitReady(context.Background(), uds, proto.GuestPort, 200*time.Millisecond)
	if err == nil {
		t.Fatal("waitReady should time out when no guest ever listens")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("waitReady returned too early: %s", elapsed)
	}
}

func TestWaitReadyHonorsContextCancel(t *testing.T) {
	uds := filepath.Join(t.TempDir(), "vsock.sock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	if err := waitReady(ctx, uds, proto.GuestPort, 5*time.Second); err != context.Canceled {
		t.Fatalf("waitReady should return ctx error, got %v", err)
	}
}

// newInfoTestManager builds a hermetic Manager (no KVM: fake artifact files,
// SkipWarmOnStart) with the given pool size, for exercising Info's boot-path
// derivation. The returned manager is Closed via t.Cleanup.
func newInfoTestManager(t *testing.T, poolSize int) *Manager {
	t.Helper()
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	m, err := New(Config{
		FirecrackerBin:  write("firecracker"),
		KernelImagePath: write("kernel"),
		InitrdPath:      write("initrd"),
		Packs: map[string]Pack{
			"python": {RootfsPath: write("rootfs"), Interpreter: "python3"},
			"shell":  {RootfsPath: write("rootfs2"), Interpreter: "/bin/sh"},
		},
		VcpuCount:       2,
		MemSizeMib:      512,
		WorkDir:         dir,
		PoolSize:        poolSize,
		SkipWarmOnStart: true, // no snapshot build, no KVM
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestInfoBootPath pins Info's boot-path label to the state that actually
// decides a Create's path: "warm-pool" when a pool is enabled, and — with the
// pool disabled — "snapshot-restore" only when a template snapshot is
// registered, else "cold-boot". The last case is the daemon started as
// `-pool-size=-1 -skip-warm -snapshot-dir=<empty dir>` (bench cold mode),
// which must NOT be labeled snapshot-restore.
func TestInfoBootPath(t *testing.T) {
	// Pool enabled (0 -> default 3): warm-pool. Also pin the static fields.
	pooled := newInfoTestManager(t, 0)
	info := pooled.Info()
	if info.BootPath != bootPathWarmPool {
		t.Errorf("pool enabled: BootPath = %q, want %q", info.BootPath, bootPathWarmPool)
	}
	if info.GuestRAMMiB != 512 || info.GuestVCPUs != 2 {
		t.Errorf("guest shape = %d MiB / %d vCPUs, want 512 / 2", info.GuestRAMMiB, info.GuestVCPUs)
	}
	if want := []string{"python", "shell"}; len(info.Packs) != 2 || info.Packs[0] != want[0] || info.Packs[1] != want[1] {
		t.Errorf("Packs = %v, want %v (sorted)", info.Packs, want)
	}
	if info.WorkDir != pooled.cfg.WorkDir {
		t.Errorf("WorkDir = %q, want %q", info.WorkDir, pooled.cfg.WorkDir)
	}

	// Pool disabled, no snapshot registered (skip-warm, empty snapshot dir):
	// every Create cold boots, and the label must say so.
	cold := newInfoTestManager(t, -1)
	if got := cold.Info().BootPath; got != bootPathColdBoot {
		t.Errorf("pool disabled, no snapshot: BootPath = %q, want %q", got, bootPathColdBoot)
	}

	// Pool disabled with a registered snapshot: Create restores from it.
	// Register directly under the lock — snapshot registration itself is
	// covered by the snapshot tests; here only the label derivation matters.
	cold.snapshotMu.Lock()
	cold.snapshots["python"] = snapshotPaths{memFile: "mem", vmState: "state"}
	cold.snapshotMu.Unlock()
	if got := cold.Info().BootPath; got != bootPathSnapshotRestore {
		t.Errorf("pool disabled, snapshot registered: BootPath = %q, want %q", got, bootPathSnapshotRestore)
	}
}

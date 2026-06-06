package firecracker

import (
	"bufio"
	"context"
	"net"
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

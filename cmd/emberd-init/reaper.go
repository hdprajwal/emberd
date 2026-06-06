package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// childReaper is the single point that calls wait(2) when emberd-init runs as
// PID 1. PID 1 inherits every process whose parent dies before it does — a
// grandchild a user's code double-forks reparents here — and must reap each one
// or it lingers as a zombie for the life of the microVM.
//
// The wrinkle is that os/exec also wants to wait on the interpreter children
// emberd-init spawns, to collect their exit codes. Only one waiter can reap a
// given child, and a blanket wait4(-1) reaper would race os/exec and steal them.
// So the reaper owns every wait4: callers that need a child's exit status
// register its pid with run() and receive the status over a channel; processes
// nobody registered are orphans, reaped and discarded.
type childReaper struct {
	mu      sync.Mutex
	waiters map[int]chan syscall.WaitStatus
	sigCh   chan os.Signal
}

func newChildReaper() *childReaper {
	return &childReaper{waiters: make(map[int]chan syscall.WaitStatus)}
}

// start installs the SIGCHLD handler and begins reaping in the background. Call
// it once, and only when emberd-init is PID 1.
func (r *childReaper) start() {
	r.sigCh = make(chan os.Signal, 1)
	signal.Notify(r.sigCh, unix.SIGCHLD)
	go func() {
		for range r.sigCh {
			r.reap()
		}
	}()
}

// stop halts reaping and ends the background goroutine. The guest never stops
// reaping (the VM is torn down instead), so this exists for tests, which must
// not leave a process-wide wait4(-1) reaper running across cases.
func (r *childReaper) stop() {
	signal.Stop(r.sigCh)
	close(r.sigCh)
}

// reap drains every child that has exited. SIGCHLD coalesces, so one delivery
// may stand for several deaths; the loop runs until no exited child remains.
// Holding mu across the drain closes the registration race: run() also holds mu
// from cmd.Start() through registering the pid, so a child can never be reaped
// and discarded in the window before its waiter is installed.
func (r *childReaper) reap() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			// pid 0: children remain but none have exited. err (ECHILD): no
			// children at all. Either way there is nothing more to reap now.
			return
		}
		if w, ok := r.waiters[pid]; ok {
			w <- ws
			delete(r.waiters, pid)
		}
		// Otherwise an orphan we just reaped: nothing to deliver, zombie cleared.
	}
}

// run starts cmd, makes the reaper its sole waiter, and blocks until it exits or
// ctx fires. It returns the program's exit code; launchErr is non-nil only when
// the program could not be started at all (mirroring the os/exec path in
// runExec). A signaled exit — including the SIGKILL run delivers on ctx
// cancellation — reports exit code -1, matching exec.ExitError.ExitCode().
func (r *childReaper) run(ctx context.Context, cmd *exec.Cmd) (exitCode int, launchErr error) {
	// Own process group so a timeout kills the whole tree the interpreter spawns,
	// not just its leader.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	ch := make(chan syscall.WaitStatus, 1)

	r.mu.Lock()
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		return -1, err
	}
	pid := cmd.Process.Pid
	r.waiters[pid] = ch
	r.mu.Unlock()

	// Wait for the reaper to collect the child, killing the group first if ctx
	// fires. The kill turns into a SIGCHLD the reaper handles, so either branch
	// ends with a status arriving on ch.
	var ws syscall.WaitStatus
	select {
	case ws = <-ch:
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		ws = <-ch
	}

	// The reaper already collected the child, so cmd.Wait's own wait fails with
	// ECHILD — but it still joins the stdout/stderr copy goroutines and closes
	// the pipes, which is the only reason we call it.
	_ = cmd.Wait()

	if ws.Signaled() {
		return -1, nil
	}
	return ws.ExitStatus(), nil
}

package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// TestReaperRunExitCodes checks that a child reaped by the reaper (not by
// os/exec) still yields the right exit code and captured output. This is the
// path that breaks if a blanket wait4 reaper races os/exec for the child.
func TestReaperRunExitCodes(t *testing.T) {
	r := newChildReaper()
	r.start()
	defer r.stop()

	cases := []struct {
		name     string
		code     string
		wantExit int
		wantOut  string
	}{
		{"zero", "echo hi", 0, "hi\n"},
		{"nonzero", "echo oops >&2; exit 7", 7, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runExec(context.Background(), r, "/bin/sh", proto.ExecRequest{Code: tc.code})
			if res.Error != "" {
				t.Fatalf("unexpected Error: %q", res.Error)
			}
			if res.ExitCode != tc.wantExit {
				t.Fatalf("ExitCode = %d, want %d", res.ExitCode, tc.wantExit)
			}
			if res.Stdout != tc.wantOut {
				t.Fatalf("Stdout = %q, want %q", res.Stdout, tc.wantOut)
			}
		})
	}
}

// TestReaperRunTimeout checks that a timeout kills the whole process group and
// is reported as a timeout, not as a stuck call.
func TestReaperRunTimeout(t *testing.T) {
	r := newChildReaper()
	r.start()
	defer r.stop()

	start := time.Now()
	res := runExec(context.Background(), r, "/bin/sh", proto.ExecRequest{
		Code:      "sleep 5",
		TimeoutMs: 200,
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout did not fire promptly: took %s", elapsed)
	}
	if res.Error == "" || !strings.Contains(res.Error, "timed out") {
		t.Fatalf("Error = %q, want it to mention timeout", res.Error)
	}
}

// TestReaperReapsOrphan checks the core PID 1 duty: a grandchild a workload
// double-forks, which outlives its parent, is reaped instead of leaking as a
// zombie. The test marks itself a child subreaper so the orphan reparents here,
// the same way it reparents to PID 1 inside the guest.
func TestReaperReapsOrphan(t *testing.T) {
	if _, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0, 0); errno != 0 {
		t.Skipf("cannot become child subreaper: %v", errno)
	}
	defer unix.Syscall6(unix.SYS_PRCTL, unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0, 0)

	r := newChildReaper()
	r.start()
	defer r.stop()

	// The shell backgrounds a sleeper and prints its pid, then exits — orphaning
	// the sleeper, which reparents to this process. $! is the background pid.
	res := runExec(context.Background(), r, "/bin/sh", proto.ExecRequest{
		Code: "sleep 0.5 & echo $!",
	})
	if res.ExitCode != 0 {
		t.Fatalf("launcher exit = %d (err %q), want 0", res.ExitCode, res.Error)
	}
	gpid, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		t.Fatalf("parse grandchild pid from %q: %v", res.Stdout, err)
	}

	// The sleeper exits ~0.5s in; once it does, the reaper must clear it. Poll
	// until its /proc entry is gone (reaped) rather than stuck in state Z.
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, alive := procState(gpid)
		if !alive {
			return // reaped: /proc entry gone
		}
		if state == "Z" && time.Now().After(deadline) {
			t.Fatalf("grandchild %d still a zombie after 5s — not reaped", gpid)
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still present (state %q) after 5s", gpid, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// procState returns the single-letter state of a process from /proc and whether
// it still exists at all.
func procState(pid int) (state string, alive bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// Format: "pid (comm) state ...". comm may contain spaces/parens, so split
	// after the last ')'.
	s := string(b)
	if i := strings.LastIndex(s, ")"); i >= 0 {
		fields := strings.Fields(s[i+1:])
		if len(fields) > 0 {
			return fields[0], true
		}
	}
	return "", true
}

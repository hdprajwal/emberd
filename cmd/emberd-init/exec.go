package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// runExec writes the request's code to a temp file, runs it under interpreter,
// and captures the result. A non-zero exit from the user's program is a normal
// result (ExitCode set, Error empty); only a failure to run the program at all
// populates Error.
//
// reaper is the PID 1 child reaper, or nil when emberd-init runs off the guest
// (host tests, manual runs). When set, the interpreter child is reaped by the
// reaper rather than by os/exec, so a double-forked grandchild can't be mistaken
// for it; when nil, the ordinary os/exec path is used.
func runExec(ctx context.Context, reaper *childReaper, interpreter string, req proto.ExecRequest) proto.ExecResult {
	start := time.Now()

	if req.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	f, err := os.CreateTemp("", "emberd-code-*")
	if err != nil {
		return proto.ExecResult{ExitCode: -1, Error: "create code file: " + err.Error(), DurationMs: elapsed(start)}
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(req.Code); err != nil {
		f.Close()
		return proto.ExecResult{ExitCode: -1, Error: "write code file: " + err.Error(), DurationMs: elapsed(start)}
	}
	if err := f.Close(); err != nil {
		return proto.ExecResult{ExitCode: -1, Error: "close code file: " + err.Error(), DurationMs: elapsed(start)}
	}

	cmd := exec.CommandContext(ctx, interpreter, f.Name())
	cmd.Stdin = strings.NewReader(req.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode, launchErr := launchAndWait(ctx, reaper, cmd)
	res := proto.ExecResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: elapsed(start),
	}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitCode = -1
		res.Error = "execution timed out"
	case launchErr != nil:
		res.ExitCode = -1
		res.Error = launchErr.Error()
	default:
		res.ExitCode = exitCode
	}
	return res
}

// launchAndWait runs cmd to completion and returns the program's exit code.
// launchErr is non-nil only when the program could not be started at all; a
// non-zero or signaled exit is a normal result, not an error. The reaper path
// and the plain os/exec path resolve these identically (a signaled exit is exit
// code -1, exactly what exec.ExitError.ExitCode reports).
func launchAndWait(ctx context.Context, reaper *childReaper, cmd *exec.Cmd) (exitCode int, launchErr error) {
	if reaper != nil {
		return reaper.run(ctx, cmd)
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

func elapsed(start time.Time) int {
	return int(time.Since(start).Milliseconds())
}

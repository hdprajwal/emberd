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
func runExec(ctx context.Context, interpreter string, req proto.ExecRequest) proto.ExecResult {
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

	runErr := cmd.Run()
	res := proto.ExecResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: elapsed(start),
	}

	switch {
	case runErr == nil:
		res.ExitCode = 0
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitCode = -1
		res.Error = "execution timed out"
	default:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
			res.Error = runErr.Error()
		}
	}
	return res
}

func elapsed(start time.Time) int {
	return int(time.Since(start).Milliseconds())
}

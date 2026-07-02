package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hdprajwal/emberd/pkg/proto"
)

func TestRunExecSuccess(t *testing.T) {
	res := runExec(context.Background(), nil, "python3", proto.ExecRequest{
		Code: "print('hello world')",
	})
	if res.Error != "" {
		t.Fatalf("unexpected Error: %q", res.Error)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Stdout != "hello world\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "hello world\n")
	}
}

func TestRunExecStdin(t *testing.T) {
	res := runExec(context.Background(), nil, "python3", proto.ExecRequest{
		Code:  "import sys; sys.stdout.write(sys.stdin.read().upper())",
		Stdin: "abc",
	})
	if res.ExitCode != 0 || res.Stdout != "ABC" {
		t.Fatalf("got exit=%d stdout=%q, want exit=0 stdout=%q", res.ExitCode, res.Stdout, "ABC")
	}
}

func TestRunExecNonZeroExit(t *testing.T) {
	res := runExec(context.Background(), nil, "python3", proto.ExecRequest{
		Code: "import sys; sys.stderr.write('boom'); sys.exit(3)",
	})
	if res.Error != "" {
		t.Fatalf("non-zero exit should not set Error, got %q", res.Error)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Fatalf("Stderr = %q, want it to contain %q", res.Stderr, "boom")
	}
}

func TestRunExecDuration(t *testing.T) {
	// The guest reports both DurationMs (whole ms, for old hosts) and DurationUs
	// (whole µs). They come from one clock reading, so ms must equal us/1000.
	cases := []struct {
		name string
		code string
	}{
		{name: "fast", code: "pass"},
		{name: "sleeps ~50ms", code: "import time; time.sleep(0.05)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runExec(context.Background(), nil, "python3", proto.ExecRequest{Code: tc.code})
			if res.Error != "" {
				t.Fatalf("unexpected Error: %q", res.Error)
			}
			if res.DurationUs <= 0 {
				t.Fatalf("DurationUs = %d, want > 0", res.DurationUs)
			}
			if got, want := res.DurationMs, int(res.DurationUs/1000); got != want {
				t.Fatalf("DurationMs = %d, want %d (from DurationUs %d)", got, want, res.DurationUs)
			}
		})
	}
}

func TestRunExecTimeout(t *testing.T) {
	res := runExec(context.Background(), nil, "python3", proto.ExecRequest{
		Code:      "import time; time.sleep(5)",
		TimeoutMs: 200,
	})
	if res.Error == "" {
		t.Fatalf("expected a timeout Error, got none (exit=%d)", res.ExitCode)
	}
	if !strings.Contains(res.Error, "timed out") {
		t.Fatalf("Error = %q, want it to mention timeout", res.Error)
	}
}

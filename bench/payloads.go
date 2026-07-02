package main

import (
	"fmt"
	"strings"
)

// kib and mib are byte-size units used throughout the payload matrix
// (bench-v2-spec.md §7).
const (
	kib = 1024
	mib = 1024 * kib
)

// Payload is a named, per-pack code snippet with optional stdin and an
// expected-result assertion (bench-v2-spec.md §7). Scenarios exec a
// Payload's Code (with Stdin and TimeoutMs, when set) and check the result
// against Assert before treating any sample as valid — the bench doubles as
// a smoke test, so a wrong result must never be silently timed.
type Payload struct {
	// Name is the stable key used in JSON output and error messages.
	Name string
	// Code is the source submitted as the exec request body.
	Code string
	// Stdin, if non-empty, is piped to the program.
	Stdin string
	// TimeoutMs, if non-zero, bounds the exec (0 leaves the daemon default).
	TimeoutMs int
	// Assert checks an exec outcome against this payload's expected result
	// (§7's assertion column). Every payload in the matrix has one.
	Assert func(ExecOutcome) error
}

// padCode pads base with ASCII "#"-comment lines until the result is within
// a few bytes of targetBytes. It never introduces quotes or backslashes, so
// the output stays safe to embed in a JSON request body at any size
// (bench-v2-spec.md §7). padCode is a pure function of (base, targetBytes):
// identical inputs always produce identical output. If base (normalized to
// end in exactly one newline) is already at or beyond targetBytes, it is
// returned unchanged — padding never truncates the base snippet.
func padCode(base string, targetBytes int) string {
	normalized := strings.TrimRight(base, "\n") + "\n"
	if len(normalized) >= targetBytes {
		return normalized
	}

	const (
		prefix      = "# "
		fillChar    = "x"
		maxLineFill = 76 // keeps each padding line at a readable ~79 chars
	)

	var b strings.Builder
	b.WriteString(normalized)

	remaining := targetBytes - b.Len()
	for remaining > len(prefix)+1 {
		fillLen := remaining - len(prefix) - 1 // -1 reserves the line's newline
		if fillLen > maxLineFill {
			fillLen = maxLineFill
		}
		line := prefix + strings.Repeat(fillChar, fillLen) + "\n"
		b.WriteString(line)
		remaining -= len(line)
	}
	return b.String()
}

// assertStdout requires exit code 0 and stdout exactly equal to want.
func assertStdout(want string) func(ExecOutcome) error {
	return func(o ExecOutcome) error {
		if o.ExitCode != 0 {
			return fmt.Errorf("exit code = %d, want 0", o.ExitCode)
		}
		if o.Stdout != want {
			return fmt.Errorf("stdout = %q, want %q", o.Stdout, want)
		}
		return nil
	}
}

// assertStdoutLen requires exit code 0 and a stdout of exactly wantLen
// bytes; content is unchecked, for bulk-output payloads where only size
// matters.
func assertStdoutLen(wantLen int) func(ExecOutcome) error {
	return func(o ExecOutcome) error {
		if o.ExitCode != 0 {
			return fmt.Errorf("exit code = %d, want 0", o.ExitCode)
		}
		if len(o.Stdout) != wantLen {
			return fmt.Errorf("stdout length = %d, want %d", len(o.Stdout), wantLen)
		}
		return nil
	}
}

// assertExitCode requires exit code want; stdout content is unchecked.
func assertExitCode(want int) func(ExecOutcome) error {
	return func(o ExecOutcome) error {
		if o.ExitCode != want {
			return fmt.Errorf("exit code = %d, want %d", o.ExitCode, want)
		}
		return nil
	}
}

// assertExitCodeNoError requires exit code want and an empty Error field.
// Per pkg/proto semantics a non-zero exit from the user's program is a
// normal result, not a transport error (bench-v2-spec.md §7, exit-3).
func assertExitCodeNoError(want int) func(ExecOutcome) error {
	return func(o ExecOutcome) error {
		if o.ExitCode != want {
			return fmt.Errorf("exit code = %d, want %d", o.ExitCode, want)
		}
		if o.Error != "" {
			return fmt.Errorf("error = %q, want empty", o.Error)
		}
		return nil
	}
}

// timedOutError is the exact error string emberd-init reports when a
// timeout_ms deadline fires (cmd/emberd-init/exec.go).
const timedOutError = "execution timed out"

// assertTimedOut requires the daemon's timeout-enforcement result shape:
// exit code -1 and Error == "execution timed out".
func assertTimedOut() func(ExecOutcome) error {
	return func(o ExecOutcome) error {
		if o.ExitCode != -1 {
			return fmt.Errorf("exit code = %d, want -1", o.ExitCode)
		}
		if o.Error != timedOutError {
			return fmt.Errorf("error = %q, want %q", o.Error, timedOutError)
		}
		return nil
	}
}

// helloPythonCode is the python "hello" payload's source, also the base
// snippet padCode pads for code-64kb and code-900kb.
const helloPythonCode = "print(\"hello world\")\n"

// stdinPayloadBytes is the stdin size for the stdin-64kb payload (§7): 64
// KiB.
const stdinPayloadBytes = 64 * kib

// PythonPayloads returns the full python-pack payload matrix in the order
// given by bench-v2-spec.md §7. Every payload has an Assert; scenarios
// other than cold-boot/ttfr/exec (workloads, memory — not implemented by
// this package yet) will draw on the payloads beyond "hello".
func PythonPayloads() []Payload {
	return []Payload{
		{
			Name:   "hello",
			Code:   helloPythonCode,
			Assert: assertStdout("hello world\n"),
		},
		{
			Name:   "imports",
			Code:   "import json, re, math, os, sys, itertools, functools, datetime, collections\nprint(\"ok\")\n",
			Assert: assertStdout("ok\n"),
		},
		{
			Name:   "code-64kb",
			Code:   padCode(helloPythonCode, 64*kib),
			Assert: assertStdout("hello world\n"),
		},
		{
			Name:   "code-900kb",
			Code:   padCode(helloPythonCode, 900*kib),
			Assert: assertExitCode(0),
		},
		{
			Name:   "out-1kb",
			Code:   "import sys\nsys.stdout.write(\"x\" * 1024)\n",
			Assert: assertStdoutLen(1024),
		},
		{
			Name:   "out-1mb",
			Code:   "import sys\nsys.stdout.write(\"x\" * (1024 * 1024))\n",
			Assert: assertStdoutLen(mib),
		},
		{
			Name:   "stdin-64kb",
			Code:   "import sys\nprint(len(sys.stdin.read()))\n",
			Stdin:  strings.Repeat("y", stdinPayloadBytes),
			Assert: assertStdout(fmt.Sprintf("%d\n", stdinPayloadBytes)),
		},
		{
			Name:   "exit-3",
			Code:   "import sys\nsys.exit(3)\n",
			Assert: assertExitCodeNoError(3),
		},
		{
			Name:      "timeout-1s",
			Code:      "import time\ntime.sleep(10)\n",
			TimeoutMs: 1000,
			Assert:    assertTimedOut(),
		},
		{
			// Used only by the memory scenario (bench-v2-spec.md §6.7, not
			// implemented by this task): allocate and touch 64 MiB, sleep
			// 2s so a concurrent sampler can catch loaded PSS, print done.
			Name: "mem-touch",
			Code: "data = bytearray(64 * 1024 * 1024)\n" +
				"for i in range(0, len(data), 4096):\n" +
				"    data[i] = 1\n" +
				"import time\n" +
				"time.sleep(2)\n" +
				"print(\"done\")\n",
			Assert: assertExitCode(0),
		},
	}
}

// helloShellCode is the shell-pack "hello" payload's source (bench-v2-spec.md
// §7: "Shell (shell pack) payload: hello = echo hello").
const helloShellCode = "echo hello\n"

// ShellPayloads returns the shell-pack payload matrix: just "hello" — the
// shell pack skips python-specific payloads (bench-v2-spec.md §6.8).
func ShellPayloads() []Payload {
	return []Payload{
		{
			Name:   "hello",
			Code:   helloShellCode,
			Assert: assertStdout("hello\n"),
		},
	}
}

// PayloadsForPack returns the payload matrix for pack (bench-v2-spec.md §7,
// §6.8). Any pack other than "shell" gets the python matrix; pkg/sandbox's
// Manager.Create is the source of truth for which pack names are actually
// valid.
func PayloadsForPack(pack string) []Payload {
	if pack == "shell" {
		return ShellPayloads()
	}
	return PythonPayloads()
}

// HelloPayload returns pack's "hello" payload — the one cold-boot, ttfr,
// and exec all share (bench-v2-spec.md §6.8). It panics if pack's matrix
// has no "hello" entry, which would be a bug in PayloadsForPack rather than
// a runtime condition a scenario could recover from.
func HelloPayload(pack string) Payload {
	for _, p := range PayloadsForPack(pack) {
		if p.Name == "hello" {
			return p
		}
	}
	panic("bench: no hello payload for pack " + pack)
}

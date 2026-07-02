package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/hdprajwal/emberd/pkg/api"
)

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestPadCodeSizeBounds(t *testing.T) {
	for _, target := range []int{64 * kib, 900 * kib, len(helloPythonCode) + 100, len(helloPythonCode) + 1000} {
		got := padCode(helloPythonCode, target)
		if diff := abs(len(got) - target); diff > 64 {
			t.Errorf("padCode(base, %d): len = %d, diff = %d, want within 64 bytes of target", target, len(got), diff)
		}
	}
}

func TestPadCodeTargetSmallerThanBase(t *testing.T) {
	// A target below the normalized base length can't shrink the base —
	// padCode returns it unchanged rather than truncating code.
	got := padCode(helloPythonCode, 4)
	want := strings.TrimRight(helloPythonCode, "\n") + "\n"
	if got != want {
		t.Errorf("padCode(base, 4) = %q, want %q", got, want)
	}
}

func TestPadCodeDeterministic(t *testing.T) {
	a := padCode(helloPythonCode, 64*kib)
	b := padCode(helloPythonCode, 64*kib)
	if a != b {
		t.Error("padCode is not a pure function of (base, targetBytes) — repeated calls differ")
	}
}

func TestPadCodeContainsBaseIntact(t *testing.T) {
	got := padCode(helloPythonCode, 64*kib)
	trimmed := strings.TrimRight(helloPythonCode, "\n")
	if !strings.HasPrefix(got, trimmed) {
		t.Errorf("padCode output does not start with the base snippet %q", trimmed)
	}
	if !strings.Contains(got, trimmed) {
		t.Errorf("padCode output does not contain the base snippet %q intact", trimmed)
	}
}

// TestPadCodeValidPythonComments checks that everything padCode appends
// beyond the base snippet is a well-formed "#"-comment line built only from
// ASCII characters with no quotes or backslashes — the property that keeps
// padded code both valid Python (comments never affect parsing) and safe to
// JSON-encode predictably at any size (bench-v2-spec.md §7, §10).
func TestPadCodeValidPythonComments(t *testing.T) {
	got := padCode(helloPythonCode, 64*kib)
	normalized := strings.TrimRight(helloPythonCode, "\n") + "\n"
	padding := strings.TrimPrefix(got, normalized)
	if padding == "" {
		t.Fatal("expected non-empty padding at 64 KiB target")
	}

	for _, line := range strings.Split(strings.TrimRight(padding, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "# ") {
			t.Fatalf("padding line %q is not a %q-comment line", line, "#")
		}
		for _, r := range line {
			if r > unicode.MaxASCII {
				t.Fatalf("padding line %q contains non-ASCII rune %q", line, r)
			}
			if r == '"' || r == '\'' || r == '\\' {
				t.Fatalf("padding line %q contains a quote or backslash", line)
			}
		}
	}
}

func findPayload(t *testing.T, payloads []Payload, name string) Payload {
	t.Helper()
	for _, p := range payloads {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("payload %q not found", name)
	return Payload{}
}

// TestCode900kbPayloadJSONUnderMaxExecBody verifies the padded code-900kb
// payload, once embedded in an exec request body and JSON-marshaled (which
// escapes every newline and quote), still fits under maxExecBody. That
// constant is unexported in pkg/api (1<<20, 1 MiB); it is mirrored here
// per bench-v2-spec.md §10 rather than exported solely for this test.
func TestCode900kbPayloadJSONUnderMaxExecBody(t *testing.T) {
	const maxExecBody = 1 << 20 // pkg/api.maxExecBody

	payload := findPayload(t, PythonPayloads(), "code-900kb")

	body, err := json.Marshal(api.ExecRequest{
		Code:      payload.Code,
		Stdin:     payload.Stdin,
		TimeoutMs: payload.TimeoutMs,
	})
	if err != nil {
		t.Fatalf("marshal exec request: %v", err)
	}
	if len(body) >= maxExecBody {
		t.Fatalf("code-900kb exec request body = %d bytes, want < maxExecBody (%d)", len(body), maxExecBody)
	}
	t.Logf("code-900kb exec request body = %d bytes (%.1f%% of maxExecBody)", len(body), 100*float64(len(body))/float64(maxExecBody))
}

func TestPythonPayloadsMatchSpecTable(t *testing.T) {
	want := []string{
		"hello", "imports", "code-64kb", "code-900kb", "out-1kb", "out-1mb",
		"stdin-64kb", "exit-3", "timeout-1s", "mem-touch",
	}
	got := make([]string, 0, len(want))
	for _, p := range PythonPayloads() {
		got = append(got, p.Name)
		if p.Assert == nil {
			t.Errorf("payload %q has no Assert", p.Name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PythonPayloads() names = %v, want %v", got, want)
	}
}

func TestShellPayloadsHelloOnly(t *testing.T) {
	got := ShellPayloads()
	if len(got) != 1 || got[0].Name != "hello" {
		t.Fatalf("ShellPayloads() = %+v, want exactly one payload named %q", got, "hello")
	}
	if got[0].Code != "echo hello\n" {
		t.Errorf("ShellPayloads()[0].Code = %q, want %q", got[0].Code, "echo hello\n")
	}
}

func TestPayloadsForPackShellSkipsPythonPayloads(t *testing.T) {
	shell := PayloadsForPack("shell")
	for _, p := range shell {
		if p.Name == "imports" || p.Name == "mem-touch" {
			t.Errorf("PayloadsForPack(shell) unexpectedly includes python-only payload %q", p.Name)
		}
	}
}

func TestHelloPayload(t *testing.T) {
	if p := HelloPayload("python"); p.Name != "hello" || p.Code != helloPythonCode {
		t.Errorf("HelloPayload(python) = %+v, want the python hello payload", p)
	}
	if p := HelloPayload("shell"); p.Name != "hello" || p.Code != "echo hello\n" {
		t.Errorf("HelloPayload(shell) = %+v, want the shell hello payload", p)
	}
}

func TestHelloPayloadUnknownPackFallsBackToPython(t *testing.T) {
	// PayloadsForPack treats anything but "shell" as python; HelloPayload
	// must not panic for an unrecognized-but-non-shell name.
	p := HelloPayload("bogus-pack")
	if p.Name != "hello" {
		t.Errorf("HelloPayload(bogus-pack).Name = %q, want hello", p.Name)
	}
}

// TestPayloadAssertions exercises each assertion helper against both a
// matching and a mismatching ExecOutcome, so a real assertion bug (e.g. an
// inverted comparison) fails loudly here instead of showing up as a bench
// run that times a wrong result.
func TestPayloadAssertions(t *testing.T) {
	payloads := make(map[string]Payload)
	for _, p := range PythonPayloads() {
		payloads[p.Name] = p
	}

	cases := []struct {
		name    string
		payload string
		outcome ExecOutcome
		wantErr bool
	}{
		{"hello matches", "hello", ExecOutcome{ExitCode: 0, Stdout: "hello world\n"}, false},
		{"hello wrong stdout", "hello", ExecOutcome{ExitCode: 0, Stdout: "nope\n"}, true},
		{"hello nonzero exit", "hello", ExecOutcome{ExitCode: 1, Stdout: "hello world\n"}, true},
		{"imports matches", "imports", ExecOutcome{ExitCode: 0, Stdout: "ok\n"}, false},
		{"out-1kb right length", "out-1kb", ExecOutcome{ExitCode: 0, Stdout: strings.Repeat("x", 1024)}, false},
		{"out-1kb wrong length", "out-1kb", ExecOutcome{ExitCode: 0, Stdout: strings.Repeat("x", 512)}, true},
		{"exit-3 matches", "exit-3", ExecOutcome{ExitCode: 3}, false},
		{"exit-3 with error set", "exit-3", ExecOutcome{ExitCode: 3, Error: "boom"}, true},
		{"exit-3 wrong code", "exit-3", ExecOutcome{ExitCode: 1}, true},
		{"timeout-1s matches", "timeout-1s", ExecOutcome{ExitCode: -1, Error: "execution timed out"}, false},
		{"timeout-1s wrong exit code", "timeout-1s", ExecOutcome{ExitCode: 0, Error: "execution timed out"}, true},
		{"timeout-1s wrong error text", "timeout-1s", ExecOutcome{ExitCode: -1, Error: "boom"}, true},
		{"stdin-64kb matches", "stdin-64kb", ExecOutcome{ExitCode: 0, Stdout: "65536\n"}, false},
		{"stdin-64kb wrong length", "stdin-64kb", ExecOutcome{ExitCode: 0, Stdout: "1\n"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := payloads[c.payload]
			if !ok {
				t.Fatalf("payload %q not found", c.payload)
			}
			err := p.Assert(c.outcome)
			if (err != nil) != c.wantErr {
				t.Errorf("%s.Assert(%+v) error = %v, wantErr %v", c.payload, c.outcome, err, c.wantErr)
			}
		})
	}
}

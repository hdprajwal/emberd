package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestScenarioJSONKey(t *testing.T) {
	cases := map[string]string{
		"cold-boot":  "cold_boot",
		"ttfr":       "ttfr",
		"conc-sweep": "conc_sweep",
		"churn":      "churn",
	}
	for name, want := range cases {
		if got := scenarioJSONKey(name); got != want {
			t.Errorf("scenarioJSONKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFilesystemTimestamp(t *testing.T) {
	got := filesystemTimestamp("2026-07-01T22:15:30Z")
	if got != "20260701T221530Z" {
		t.Errorf("filesystemTimestamp() = %q, want %q", got, "20260701T221530Z")
	}
}

func TestFilesystemTimestampInvalid(t *testing.T) {
	if got := filesystemTimestamp("not-a-timestamp"); got != "unknown-timestamp" {
		t.Errorf("filesystemTimestamp() = %q, want %q", got, "unknown-timestamp")
	}
}

func TestReportMarshalOmitsUnrunScenarios(t *testing.T) {
	r := NewReport(
		Provenance{Timestamp: "2026-07-01T22:15:30Z", GitCommit: "abc1234", BenchFlags: map[string]any{"addr": "x"}},
		Env{OS: "linux", Arch: "amd64", GuestRAMMiB: "unknown (daemon has no /info endpoint)"},
		BinaryKB{Emberd: 1, EmberdInit: 2},
	)

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded["scenarios"]; ok {
		t.Errorf("scenarios key present with no scenarios run: %s", data)
	}
	for _, key := range []string{"provenance", "env", "binary_kb"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing top-level key %q in %s", key, data)
		}
	}
}

func TestReportMarshalIncludesRunScenarios(t *testing.T) {
	r := NewReport(Provenance{Timestamp: "2026-07-01T22:15:30Z", GitCommit: "abc1234"}, Env{}, BinaryKB{})
	r.AddScenario("cold-boot", ScenarioResult{JSON: map[string]any{"n": 5}, Markdown: "## Cold boot\n"})

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"cold_boot"`) {
		t.Errorf("expected JSON key \"cold_boot\" in %s", data)
	}
}

func TestWriteMarkdownIncludesScenarioSections(t *testing.T) {
	r := NewReport(Provenance{Timestamp: "2026-07-01T22:15:30Z", GitCommit: "abc1234", BenchFlags: map[string]any{}}, Env{}, BinaryKB{})
	r.AddScenario("cold-boot", ScenarioResult{Markdown: "## Cold boot\n\nsome content\n"})

	var buf bytes.Buffer
	r.WriteMarkdown(&buf)
	out := buf.String()
	if !strings.Contains(out, "## Cold boot") {
		t.Errorf("markdown missing scenario section:\n%s", out)
	}
	if !strings.Contains(out, "some content") {
		t.Errorf("markdown missing scenario content:\n%s", out)
	}
}

func TestWriteMarkdownNoScenarios(t *testing.T) {
	r := NewReport(Provenance{Timestamp: "2026-07-01T22:15:30Z", GitCommit: "abc1234", BenchFlags: map[string]any{}}, Env{}, BinaryKB{})

	var buf bytes.Buffer
	r.WriteMarkdown(&buf)
	if !strings.Contains(buf.String(), "No scenarios ran") {
		t.Errorf("expected 'No scenarios ran' note, got:\n%s", buf.String())
	}
}

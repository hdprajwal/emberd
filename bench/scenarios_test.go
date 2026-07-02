package main

import (
	"strings"
	"testing"
	"time"
)

func TestTimeMsSubMillisecondPrecision(t *testing.T) {
	// timeMs must not quantize to whole milliseconds the way
	// Duration.Milliseconds() does (bench-v2-spec.md §4); a fixed elapsed
	// duration should round-trip with fractional precision intact.
	start := time.Now().Add(-1234567 * time.Microsecond) // 1234.567 ms ago
	got := timeMs(start)
	if got < 1234.5 || got > 1240 {
		t.Fatalf("timeMs = %v, want ~1234.567 (fractional ms preserved, some scheduling slack allowed)", got)
	}
}

func TestTruncateShortStringUnchanged(t *testing.T) {
	if got := truncate("hello", 200); got != "hello" {
		t.Errorf("truncate(short) = %q, want unchanged", got)
	}
}

func TestTruncateLongStringReportsTotalLength(t *testing.T) {
	s := strings.Repeat("x", 1000)
	got := truncate(s, 10)
	if !strings.HasPrefix(got, strings.Repeat("x", 10)) {
		t.Errorf("truncate(long) = %q, want to start with 10 x's", got)
	}
	if !strings.Contains(got, "1000 bytes total") {
		t.Errorf("truncate(long) = %q, want it to report the original length", got)
	}
}

func TestCheckPayloadNilAssertPasses(t *testing.T) {
	p := Payload{Name: "no-assert"}
	if err := checkPayload(p, 0, ExecOutcome{}); err != nil {
		t.Errorf("checkPayload with nil Assert = %v, want nil", err)
	}
}

func TestCheckPayloadMismatchNamesPayloadIterationAndResponse(t *testing.T) {
	hello := HelloPayload("python")
	err := checkPayload(hello, 7, ExecOutcome{ExitCode: 1, Stdout: "boom"})
	if err == nil {
		t.Fatal("checkPayload(mismatch) = nil, want error")
	}
	for _, want := range []string{"hello", "iteration 7", "exit_code=1", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("checkPayload error %q missing %q", err.Error(), want)
		}
	}
}

func TestCheckPayloadMatchPasses(t *testing.T) {
	hello := HelloPayload("python")
	if err := checkPayload(hello, 0, ExecOutcome{ExitCode: 0, Stdout: "hello world\n"}); err != nil {
		t.Errorf("checkPayload(match) = %v, want nil", err)
	}
}

func TestStatsTableRendersGatedCells(t *testing.T) {
	table := statsTable([]statsRow{
		{"create", NewStats([]float64{1, 2, 3})}, // n=3: p95/p99 gated out
	})
	if !strings.Contains(table, "| create |") {
		t.Errorf("statsTable missing row label:\n%s", table)
	}
	if !strings.Contains(table, "—") {
		t.Errorf("statsTable with n=3 should render gated p95/p99 as \"—\":\n%s", table)
	}
}

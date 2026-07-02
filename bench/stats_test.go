package main

import (
	"testing"
)

func TestNewStatsEmpty(t *testing.T) {
	s := NewStats(nil)
	if s.N != 0 {
		t.Fatalf("N = %d, want 0", s.N)
	}
	if s.P95Ms != nil || s.P99Ms != nil {
		t.Fatalf("gated fields must be nil for empty input, got p95=%v p99=%v", s.P95Ms, s.P99Ms)
	}
}

func TestNewStatsPercentiles(t *testing.T) {
	// 100 samples: 1.0, 2.0, ..., 100.0 ms. Nearest-rank with
	// idx = round(p/100*(n-1)) gives deterministic, hand-checkable results.
	samples := make([]float64, 100)
	for i := range samples {
		samples[i] = float64(i + 1)
	}

	s := NewStats(samples)
	if s.N != 100 {
		t.Fatalf("N = %d, want 100", s.N)
	}
	if s.MeanMs != 50.5 {
		t.Fatalf("MeanMs = %v, want 50.5", s.MeanMs)
	}
	if s.MinMs != 1 {
		t.Fatalf("MinMs = %v, want 1", s.MinMs)
	}
	if s.MaxMs != 100 {
		t.Fatalf("MaxMs = %v, want 100", s.MaxMs)
	}
	if s.P50Ms != 51 {
		t.Fatalf("P50Ms = %v, want 51", s.P50Ms)
	}
	if s.P95Ms == nil {
		t.Fatalf("P95Ms is nil, want present at n=100")
	} else if *s.P95Ms != 95 {
		t.Fatalf("P95Ms = %v, want 95", *s.P95Ms)
	}
	if s.P99Ms == nil {
		t.Fatalf("P99Ms is nil, want present at n=100")
	} else if *s.P99Ms != 99 {
		t.Fatalf("P99Ms = %v, want 99", *s.P99Ms)
	}
}

func TestNewStatsUnsorted(t *testing.T) {
	// Percentiles/min/max must not depend on input order, and the input
	// slice must not be mutated (callers may reuse it).
	samples := []float64{5, 1, 4, 2, 3}
	orig := append([]float64(nil), samples...)

	s := NewStats(samples)
	if s.MinMs != 1 || s.MaxMs != 5 {
		t.Fatalf("MinMs/MaxMs = %v/%v, want 1/5", s.MinMs, s.MaxMs)
	}
	for i := range samples {
		if samples[i] != orig[i] {
			t.Fatalf("NewStats mutated its input slice: got %v, want %v", samples, orig)
		}
	}
}

func TestNewStatsGating(t *testing.T) {
	mkSamples := func(n int) []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = float64(i + 1)
		}
		return s
	}

	cases := []struct {
		n          int
		wantP95Nil bool
		wantP99Nil bool
	}{
		{n: 19, wantP95Nil: true, wantP99Nil: true},
		{n: 20, wantP95Nil: false, wantP99Nil: true},
		{n: 99, wantP95Nil: false, wantP99Nil: true},
		{n: 100, wantP95Nil: false, wantP99Nil: false},
	}
	for _, c := range cases {
		s := NewStats(mkSamples(c.n))
		if (s.P95Ms == nil) != c.wantP95Nil {
			t.Errorf("n=%d: P95Ms nil = %v, want %v", c.n, s.P95Ms == nil, c.wantP95Nil)
		}
		if (s.P99Ms == nil) != c.wantP99Nil {
			t.Errorf("n=%d: P99Ms nil = %v, want %v", c.n, s.P99Ms == nil, c.wantP99Nil)
		}
	}
}

func TestFormatMs(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{v: 10, want: "10 ms"},
		{v: 10.6, want: "11 ms"},
		{v: 9.99, want: "9.99 ms"},
		{v: 0.5, want: "0.50 ms"},
		{v: 123.456, want: "123 ms"},
	}
	for _, c := range cases {
		if got := FormatMs(c.v); got != c.want {
			t.Errorf("FormatMs(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestFormatGatedMs(t *testing.T) {
	if got := FormatGatedMs(nil); got != "—" {
		t.Errorf("FormatGatedMs(nil) = %q, want %q", got, "—")
	}
	v := 12.0
	if got := FormatGatedMs(&v); got != "12 ms" {
		t.Errorf("FormatGatedMs(&12.0) = %q, want %q", got, "12 ms")
	}
}

func TestFormatThroughputMBs(t *testing.T) {
	if got := FormatThroughputMBs(1.23); got != "1.2 MB/s" {
		t.Errorf("FormatThroughputMBs(1.23) = %q, want %q", got, "1.2 MB/s")
	}
	if got := FormatThroughputMBs(0); got != "0.0 MB/s" {
		t.Errorf("FormatThroughputMBs(0) = %q, want %q", got, "0.0 MB/s")
	}
}

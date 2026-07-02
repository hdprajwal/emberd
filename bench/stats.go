package main

import (
	"fmt"
	"sort"
)

// Minimum sample counts required before a percentile is trustworthy enough
// to report (bench-v2-spec.md §5). Below the threshold the field is omitted
// from JSON and rendered as "—" in Markdown — never a misleading zero.
const (
	minSamplesP95 = 20
	minSamplesP99 = 100
)

// Stats summarizes a distribution of millisecond samples: mean, gated
// percentiles, and extremes. Every field except N is a float64 millisecond
// value at sub-millisecond precision (see client.go for how samples are
// measured).
type Stats struct {
	N      int      `json:"n"`
	MeanMs float64  `json:"mean_ms"`
	P50Ms  float64  `json:"p50_ms"`
	P95Ms  *float64 `json:"p95_ms,omitempty"`
	P99Ms  *float64 `json:"p99_ms,omitempty"`
	MinMs  float64  `json:"min_ms"`
	MaxMs  float64  `json:"max_ms"`
}

// NewStats computes Stats over samples using nearest-rank percentiles. p95
// is omitted below minSamplesP95 samples and p99 below minSamplesP99,
// per §5's gating rule. An empty slice yields a zero-value Stats (N=0);
// callers must not treat that as "all zero" — check N before using it.
func NewStats(samples []float64) Stats {
	n := len(samples)
	if n == 0 {
		return Stats{}
	}

	sorted := make([]float64, n)
	copy(sorted, samples)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}

	s := Stats{
		N:      n,
		MeanMs: sum / float64(n),
		P50Ms:  nearestRank(sorted, 50),
		MinMs:  sorted[0],
		MaxMs:  sorted[n-1],
	}
	if n >= minSamplesP95 {
		v := nearestRank(sorted, 95)
		s.P95Ms = &v
	}
	if n >= minSamplesP99 {
		v := nearestRank(sorted, 99)
		s.P99Ms = &v
	}
	return s
}

// nearestRank returns the p-th percentile of sorted (already ascending)
// using nearest-rank interpolation: idx = round(p/100 * (n-1)).
func nearestRank(sorted []float64, p float64) float64 {
	n := len(sorted)
	idx := int(p/100*float64(n-1) + 0.5)
	if idx >= n {
		idx = n - 1
	}
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

// FormatMs renders a millisecond value per the Markdown formatting rule
// (§5): values >= 10ms print with no decimal places, smaller values print
// with two, so sub-millisecond differences (e.g. warm-pool creates) stay
// visible.
func FormatMs(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.2f ms", v)
}

// FormatGatedMs renders a gated percentile cell: "—" when the value was
// omitted for insufficient sample count, else FormatMs.
func FormatGatedMs(v *float64) string {
	if v == nil {
		return "—"
	}
	return FormatMs(*v)
}

// FormatThroughputMBs renders a throughput value in MB/s to one decimal
// place, per §5.
func FormatThroughputMBs(v float64) string {
	return fmt.Sprintf("%.1f MB/s", v)
}

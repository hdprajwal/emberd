package main

import (
	"fmt"
	"strings"
	"time"
)

// settleDelay is the fixed sleep after a timed teardown, so sample i never
// overlaps sample i-1's teardown (Firecracker process exit, tap/CID
// cleanup). It is never included in a timed window (bench-v2-spec.md §4).
// The churn scenario (not implemented by this package) is the one
// documented exemption.
const settleDelay = 100 * time.Millisecond

func init() {
	registerScenario("cold-boot", runColdBoot)
	registerScenario("ttfr", runTTFR)
	registerScenario("exec", runExecScenario)
}

// timeMs returns the elapsed time since start in fractional milliseconds.
// Every sample in this package is measured this way — never
// time.Duration.Milliseconds(), which would quantize to whole milliseconds
// and defeat the sub-millisecond precision bench-v2-spec.md §4 requires.
func timeMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

// truncate shortens s for error messages so a large payload's stdout
// doesn't flood stderr on a mismatch; the true length is still reported.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s...(%d bytes total)", s[:n], len(s))
}

// checkPayload verifies outcome against payload's expected result
// (bench-v2-spec.md §7). A non-nil return names the payload, the iteration
// it occurred on, and the actual response — on mismatch a scenario fails
// fatally rather than silently timing a wrong result.
func checkPayload(payload Payload, iteration int, outcome ExecOutcome) error {
	if payload.Assert == nil {
		return nil
	}
	if err := payload.Assert(outcome); err != nil {
		return fmt.Errorf("payload %q iteration %d: %v (actual: exit_code=%d stdout=%q stderr=%q error=%q)",
			payload.Name, iteration, err,
			outcome.ExitCode, truncate(outcome.Stdout, 200), truncate(outcome.Stderr, 200), outcome.Error)
	}
	return nil
}

// statsRow pairs a Markdown table row label with the Stats it summarizes.
type statsRow struct {
	Label string
	Stats Stats
}

// statsTable renders rows as a Markdown table with the columns and gating
// rules from bench-v2-spec.md §5: n, mean, gated p50/p95/p99, min, max.
func statsTable(rows []statsRow) string {
	var b strings.Builder
	b.WriteString("| metric | n | mean | p50 | p95 | p99 | min | max |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s |\n",
			r.Label, r.Stats.N, FormatMs(r.Stats.MeanMs), FormatMs(r.Stats.P50Ms),
			FormatGatedMs(r.Stats.P95Ms), FormatGatedMs(r.Stats.P99Ms),
			FormatMs(r.Stats.MinMs), FormatMs(r.Stats.MaxMs))
	}
	return b.String()
}

// ColdBootPackResult is one pack's cold-boot outcome: create and delete
// latency as separate sample series (bench-v2-spec.md §6.1, §9.4).
type ColdBootPackResult struct {
	Create Stats `json:"create"`
	Delete Stats `json:"delete"`
}

// ColdBootResult is the cold-boot scenario's JSON payload, keyed by
// language pack.
type ColdBootResult map[string]ColdBootPackResult

// runColdBoot times sandbox create and delete as separate series, once per
// configured pack, settling settleDelay after each delete outside the
// timed window (bench-v2-spec.md §6.1).
func runColdBoot(rc *runContext) (ScenarioResult, error) {
	result := make(ColdBootResult, len(rc.Config.Packs))
	var md strings.Builder
	md.WriteString("## Cold boot\n\n")
	md.WriteString("Sandbox create and delete latency, timed as separate series. ")
	fmt.Fprintf(&md, "%s settle after each delete, outside the timed window (bench-v2-spec.md §6.1).\n\n", settleDelay)

	for _, pack := range rc.Config.Packs {
		n := rc.Config.ColdN
		creates := make([]float64, 0, n)
		deletes := make([]float64, 0, n)

		for i := 0; i < n; i++ {
			t0 := time.Now()
			id, err := rc.Client.Create(rc.Ctx, pack)
			if err != nil {
				return ScenarioResult{}, fmt.Errorf("cold-boot[%s] iteration %d: create: %w", pack, i, err)
			}
			creates = append(creates, timeMs(t0))

			t1 := time.Now()
			if err := rc.Client.Delete(rc.Ctx, id); err != nil {
				return ScenarioResult{}, fmt.Errorf("cold-boot[%s] iteration %d: delete: %w", pack, i, err)
			}
			deletes = append(deletes, timeMs(t1))

			time.Sleep(settleDelay)
		}

		packResult := ColdBootPackResult{Create: NewStats(creates), Delete: NewStats(deletes)}
		result[pack] = packResult

		fmt.Fprintf(&md, "### %s\n\n", pack)
		md.WriteString(statsTable([]statsRow{
			{"create", packResult.Create},
			{"delete", packResult.Delete},
		}))
		md.WriteString("\n")
	}

	return ScenarioResult{JSON: result, Markdown: md.String()}, nil
}

// TTFRPackResult is one pack's time-to-first-result outcome: total session
// latency plus its three components (bench-v2-spec.md §6.2, §9.4).
type TTFRPackResult struct {
	Total     Stats `json:"total"`
	Create    Stats `json:"create"`
	FirstExec Stats `json:"first_exec"`
	Delete    Stats `json:"delete"`
}

// TTFRResult is the ttfr scenario's JSON payload, keyed by language pack.
type TTFRResult map[string]TTFRPackResult

// runTTFR times the full create -> exec(hello) -> delete session, once per
// configured pack, recording four samples per iteration and settling
// settleDelay between iterations (bench-v2-spec.md §6.2).
func runTTFR(rc *runContext) (ScenarioResult, error) {
	result := make(TTFRResult, len(rc.Config.Packs))
	var md strings.Builder
	md.WriteString("## Time-to-first-result (ttfr)\n\n")
	md.WriteString("Full session create → exec(`hello`) → delete — the E2B-comparable end-to-end number ")
	fmt.Fprintf(&md, "(bench-v2-spec.md §6.2). %s settle between iterations.\n\n", settleDelay)

	for _, pack := range rc.Config.Packs {
		hello := HelloPayload(pack)
		n := rc.Config.TTFRN

		totals := make([]float64, 0, n)
		creates := make([]float64, 0, n)
		firstExecs := make([]float64, 0, n)
		deletes := make([]float64, 0, n)

		for i := 0; i < n; i++ {
			t0 := time.Now()

			id, err := rc.Client.Create(rc.Ctx, pack)
			if err != nil {
				return ScenarioResult{}, fmt.Errorf("ttfr[%s] iteration %d: create: %w", pack, i, err)
			}
			creates = append(creates, timeMs(t0))

			te := time.Now()
			outcome, err := rc.Client.Exec(rc.Ctx, id, hello.Code, hello.Stdin, hello.TimeoutMs)
			if err != nil {
				return ScenarioResult{}, fmt.Errorf("ttfr[%s] iteration %d: exec: %w", pack, i, err)
			}
			firstExecs = append(firstExecs, timeMs(te))
			if err := checkPayload(hello, i, outcome); err != nil {
				return ScenarioResult{}, fmt.Errorf("ttfr[%s]: %w", pack, err)
			}

			td := time.Now()
			if err := rc.Client.Delete(rc.Ctx, id); err != nil {
				return ScenarioResult{}, fmt.Errorf("ttfr[%s] iteration %d: delete: %w", pack, i, err)
			}
			deletes = append(deletes, timeMs(td))

			// Total spans the whole session, measured from the same t0 as
			// create — it is not the sum of the components (which would
			// double-count nothing here, but this keeps it an independent,
			// directly-measured sample rather than a derived one).
			totals = append(totals, timeMs(t0))

			time.Sleep(settleDelay)
		}

		packResult := TTFRPackResult{
			Total:     NewStats(totals),
			Create:    NewStats(creates),
			FirstExec: NewStats(firstExecs),
			Delete:    NewStats(deletes),
		}
		result[pack] = packResult

		fmt.Fprintf(&md, "### %s\n\n", pack)
		fmt.Fprintf(&md, "**Total session (headline): %s p50, n=%d**\n\n", FormatMs(packResult.Total.P50Ms), packResult.Total.N)
		md.WriteString(statsTable([]statsRow{
			{"total", packResult.Total},
			{"create", packResult.Create},
			{"first exec", packResult.FirstExec},
			{"delete", packResult.Delete},
		}))
		md.WriteString("\n")
	}

	return ScenarioResult{JSON: result, Markdown: md.String()}, nil
}

// ExecPackResult is one pack's warm-sandbox steady-state exec outcome
// (bench-v2-spec.md §6.3, §9.4).
type ExecPackResult struct {
	API             Stats   `json:"api"`
	Guest           Stats   `json:"guest"`
	FirstExecMs     float64 `json:"first_exec_ms"`
	GuestResolution string  `json:"guest_resolution"`
}

// ExecResult is the exec scenario's JSON payload, keyed by language pack.
type ExecResult map[string]ExecPackResult

// runExecScenario execs the hello payload rc.Config.ExecN times against one
// warm sandbox per configured pack. The first exec is recorded separately
// as the warmup-floor scalar and excluded from the steady-state
// distribution (bench-v2-spec.md §4, §6.3).
func runExecScenario(rc *runContext) (ScenarioResult, error) {
	result := make(ExecResult, len(rc.Config.Packs))
	var md strings.Builder
	md.WriteString("## Exec (warm-sandbox steady-state)\n\n")
	md.WriteString("One warm sandbox per pack; n execs of `hello`. The first exec is a separate warmup-floor ")
	md.WriteString("scalar, excluded from the steady-state distribution (bench-v2-spec.md §4, §6.3).\n\n")

	for _, pack := range rc.Config.Packs {
		hello := HelloPayload(pack)

		id, err := rc.Client.Create(rc.Ctx, pack)
		if err != nil {
			return ScenarioResult{}, fmt.Errorf("exec[%s]: create: %w", pack, err)
		}

		packResult, err := execWarmSandbox(rc, pack, id, hello, rc.Config.ExecN)
		if err != nil {
			_ = rc.Client.Delete(rc.Ctx, id) // best-effort cleanup; the exec error is what matters
			return ScenarioResult{}, err
		}
		if err := rc.Client.Delete(rc.Ctx, id); err != nil {
			return ScenarioResult{}, fmt.Errorf("exec[%s]: delete: %w", pack, err)
		}
		result[pack] = packResult

		fmt.Fprintf(&md, "### %s\n\n", pack)
		fmt.Fprintf(&md, "first exec (warmup floor): %s\n\n", FormatMs(packResult.FirstExecMs))
		md.WriteString(statsTable([]statsRow{
			{"api round-trip", packResult.API},
			{"guest duration", packResult.Guest},
		}))
		md.WriteString("\n")
		md.WriteString(vsockOverheadLine(packResult))
	}

	return ScenarioResult{JSON: result, Markdown: md.String()}, nil
}

// execWarmSandbox runs n execs of hello against the already-created sandbox
// id, splitting off the first exec as the warmup scalar per §4: it captures
// interpreter page-cache warmup and is excluded from the steady-state
// distribution formed by the remaining n-1 samples.
func execWarmSandbox(rc *runContext, pack, id string, hello Payload, n int) (ExecPackResult, error) {
	if n < 1 {
		return ExecPackResult{}, fmt.Errorf("exec[%s]: -exec must be >= 1, got %d", pack, n)
	}

	t0 := time.Now()
	first, err := rc.Client.Exec(rc.Ctx, id, hello.Code, hello.Stdin, hello.TimeoutMs)
	if err != nil {
		return ExecPackResult{}, fmt.Errorf("exec[%s] iteration 0 (warmup): %w", pack, err)
	}
	firstExecMs := timeMs(t0)
	if err := checkPayload(hello, 0, first); err != nil {
		return ExecPackResult{}, fmt.Errorf("exec[%s]: %w", pack, err)
	}

	apiSamples := make([]float64, 0, n-1)
	guestSamples := make([]float64, 0, n-1)
	resolution := first.GuestResolution
	for i := 1; i < n; i++ {
		t := time.Now()
		outcome, err := rc.Client.Exec(rc.Ctx, id, hello.Code, hello.Stdin, hello.TimeoutMs)
		if err != nil {
			return ExecPackResult{}, fmt.Errorf("exec[%s] iteration %d: %w", pack, i, err)
		}
		apiSamples = append(apiSamples, timeMs(t))
		if err := checkPayload(hello, i, outcome); err != nil {
			return ExecPackResult{}, fmt.Errorf("exec[%s]: %w", pack, err)
		}
		guestSamples = append(guestSamples, outcome.GuestDurationMs)
		resolution = outcome.GuestResolution
	}

	return ExecPackResult{
		API:             NewStats(apiSamples),
		Guest:           NewStats(guestSamples),
		FirstExecMs:     firstExecMs,
		GuestResolution: resolution,
	}, nil
}

// vsockOverheadLine renders the "API - guest" vsock-overhead derivation
// (median of each series), labeled approximate whenever the guest duration
// is only ms-resolution — comparing two ms-quantized values overstates the
// derivation's precision (bench-v2-spec.md §6.3).
func vsockOverheadLine(r ExecPackResult) string {
	overhead := r.API.P50Ms - r.Guest.P50Ms
	if r.GuestResolution == "us" {
		return fmt.Sprintf("vsock overhead (api p50 − guest p50): %s\n", FormatMs(overhead))
	}
	return fmt.Sprintf("vsock overhead (api p50 − guest p50, **approximate** — guest resolution is ms): %s\n", FormatMs(overhead))
}

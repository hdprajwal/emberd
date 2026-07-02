package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
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
	registerScenario("workloads", runWorkloads)
	registerScenario("conc-sweep", runConcSweep)
	registerScenario("churn", runChurn)
	registerScenario("memory", runMemory)
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
				// Best-effort cleanup: scenario errors are fatal, so without
				// this the iteration's sandbox would stay running on the
				// daemon (same pattern as execWarmSandbox).
				_ = rc.Client.Delete(rc.Ctx, id)
				return ScenarioResult{}, fmt.Errorf("ttfr[%s] iteration %d: exec: %w", pack, i, err)
			}
			firstExecs = append(firstExecs, timeMs(te))
			if err := checkPayload(hello, i, outcome); err != nil {
				_ = rc.Client.Delete(rc.Ctx, id) // best-effort cleanup; the mismatch is what matters
				return ScenarioResult{}, fmt.Errorf("ttfr[%s]: %w", pack, err)
			}

			td := time.Now()
			if err := rc.Client.Delete(rc.Ctx, id); err != nil {
				// One best-effort retry: a transient failure here would
				// otherwise leak the sandbox, since this error is fatal.
				_ = rc.Client.Delete(rc.Ctx, id)
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

// WorkloadPayloadResult is one payload's warm-exec outcome in the workloads
// scenario: the API round-trip and guest-reported duration as separate
// sample series (bench-v2-spec.md §6.4, §9.4).
type WorkloadPayloadResult struct {
	API   Stats `json:"api"`
	Guest Stats `json:"guest"`
}

// WorkloadResult is the workloads scenario's JSON payload. Each matrix
// payload contributes an entry keyed by its underscored name (e.g.
// code_64kb); alongside those it carries derived end-to-end throughput for
// the output-heavy payloads and the timeout-enforcement overshoot
// distribution (bench-v2-spec.md §6.4, §9.4).
type WorkloadResult struct {
	// Payloads maps each payload's underscored JSON key to its result.
	Payloads map[string]WorkloadPayloadResult
	// ThroughputMBs maps out_1kb / out_1mb to end-to-end MB/s.
	ThroughputMBs map[string]float64
	// TimeoutOvershoot is the observed api_ms − timeout budget per sample
	// for the timeout-1s payload.
	TimeoutOvershoot Stats
}

// MarshalJSON flattens the per-payload entries to the top level alongside
// the reserved throughput_mb_s and timeout_overshoot keys, matching the
// schema in bench-v2-spec.md §9.4.
func (r WorkloadResult) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(r.Payloads)+2)
	for k, v := range r.Payloads {
		out[k] = v
	}
	out["throughput_mb_s"] = r.ThroughputMBs
	out["timeout_overshoot"] = r.TimeoutOvershoot
	return json.Marshal(out)
}

// payloadJSONKey maps a payload's hyphenated name to its underscored JSON
// key (e.g. "code-64kb" -> "code_64kb"), the same hyphen-to-underscore rule
// the report layer applies to scenario names (bench-v2-spec.md §9.4).
func payloadJSONKey(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// throughputMBs derives end-to-end exec throughput in MB/s from a payload's
// observed output size and its API-round-trip p50: outputBytes / api_p50.
// It is honestly the whole request's throughput (transfer + guest work), not
// an isolated vsock number — subtracting the guest duration would be invalid
// because both the API and guest measurements include the transfer
// (bench-v2-spec.md §6.4). Returns 0 when p50 is non-positive.
func throughputMBs(outputBytes int, p50Ms float64) float64 {
	if p50Ms <= 0 {
		return 0
	}
	return float64(outputBytes) / (p50Ms * 1000.0)
}

// runWorkloads execs the full payload matrix against one warm sandbox on the
// first configured pack (bench-v2-spec.md §6.8 — this scenario measures the
// runtime, not each pack's interpreter). Each payload's first exec is
// excluded as warmup (§4: the interpreter is warm but a payload's own
// imports may still be cold), and every exec is checked against the
// payload's assertion. The output-heavy payloads yield derived throughput
// and timeout-1s yields an enforcement-overshoot distribution
// (bench-v2-spec.md §6.4).
func runWorkloads(rc *runContext) (ScenarioResult, error) {
	pack := rc.Config.Packs[0]
	n := rc.Config.WorkloadN
	if n < 1 {
		return ScenarioResult{}, fmt.Errorf("workloads[%s]: -workload-n must be >= 1, got %d", pack, n)
	}

	id, err := rc.Client.Create(rc.Ctx, pack)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("workloads[%s]: create: %w", pack, err)
	}

	result := WorkloadResult{
		Payloads:      make(map[string]WorkloadPayloadResult),
		ThroughputMBs: make(map[string]float64),
	}
	outBytes := make(map[string]int)
	var rows []statsRow

	for _, payload := range PayloadsForPack(pack) {
		// mem-touch belongs to the memory scenario only (bench-v2-spec.md
		// §6.7); it is not part of the workloads loop.
		if payload.Name == "mem-touch" {
			continue
		}

		apiSamples, guestSamples, ob, err := workloadPayloadSamples(rc, pack, id, payload, n)
		if err != nil {
			_ = rc.Client.Delete(rc.Ctx, id) // best-effort cleanup; the exec/assert error is what matters
			return ScenarioResult{}, err
		}

		key := payloadJSONKey(payload.Name)
		api := NewStats(apiSamples)
		result.Payloads[key] = WorkloadPayloadResult{API: api, Guest: NewStats(guestSamples)}
		outBytes[key] = ob
		rows = append(rows, statsRow{payload.Name, api})

		if payload.Name == "timeout-1s" {
			overshoot := make([]float64, len(apiSamples))
			for i, v := range apiSamples {
				overshoot[i] = v - float64(payload.TimeoutMs)
			}
			result.TimeoutOvershoot = NewStats(overshoot)
		}
	}

	if err := rc.Client.Delete(rc.Ctx, id); err != nil {
		return ScenarioResult{}, fmt.Errorf("workloads[%s]: delete: %w", pack, err)
	}

	// Derived end-to-end throughput for the output-heavy payloads (§6.4).
	for _, name := range []string{"out-1kb", "out-1mb"} {
		key := payloadJSONKey(name)
		if pr, ok := result.Payloads[key]; ok {
			result.ThroughputMBs[key] = throughputMBs(outBytes[key], pr.API.P50Ms)
		}
	}

	var md strings.Builder
	md.WriteString("## Workloads (payload matrix)\n\n")
	fmt.Fprintf(&md, "One warm sandbox on the `%s` pack; %d execs per payload, the first of each excluded as warmup (bench-v2-spec.md §6.4). API round-trip per payload:\n\n", pack, n)
	md.WriteString(statsTable(rows))
	md.WriteString("\n")

	md.WriteString("End-to-end exec throughput (output bytes ÷ api p50 — the whole request, not an isolated vsock number):\n\n")
	for _, name := range []string{"out-1kb", "out-1mb"} {
		key := payloadJSONKey(name)
		if v, ok := result.ThroughputMBs[key]; ok {
			fmt.Fprintf(&md, "- `%s`: %s\n", name, FormatThroughputMBs(v))
		}
	}
	md.WriteString("\n")

	if result.TimeoutOvershoot.N > 0 {
		md.WriteString("Timeout enforcement overshoot (observed api − 1000 ms timeout budget):\n\n")
		md.WriteString(statsTable([]statsRow{{"timeout-1s overshoot", result.TimeoutOvershoot}}))
		md.WriteString("\n")
	}

	return ScenarioResult{JSON: result, Markdown: md.String()}, nil
}

// workloadPayloadSamples runs n execs of payload against the already-created
// warm sandbox id, excluding the first (warmup) exec per §4. It returns the
// steady-state API-round-trip samples, the guest-duration samples, and the
// stdout byte length observed on the final exec (used to derive throughput).
// Every exec — warmup included — is checked against payload's assertion, so
// a wrong result fails fatally rather than being silently timed.
func workloadPayloadSamples(rc *runContext, pack, id string, payload Payload, n int) (apiSamples, guestSamples []float64, outBytes int, err error) {
	warm, err := rc.Client.Exec(rc.Ctx, id, payload.Code, payload.Stdin, payload.TimeoutMs)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("workloads[%s] payload %q warmup exec: %w", pack, payload.Name, err)
	}
	if err := checkPayload(payload, 0, warm); err != nil {
		return nil, nil, 0, fmt.Errorf("workloads[%s]: %w", pack, err)
	}
	outBytes = len(warm.Stdout)

	apiSamples = make([]float64, 0, n-1)
	guestSamples = make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		t := time.Now()
		outcome, err := rc.Client.Exec(rc.Ctx, id, payload.Code, payload.Stdin, payload.TimeoutMs)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("workloads[%s] payload %q iteration %d: %w", pack, payload.Name, i, err)
		}
		apiSamples = append(apiSamples, timeMs(t))
		if err := checkPayload(payload, i, outcome); err != nil {
			return nil, nil, 0, fmt.Errorf("workloads[%s]: %w", pack, err)
		}
		guestSamples = append(guestSamples, outcome.GuestDurationMs)
		outBytes = len(outcome.Stdout)
	}
	return apiSamples, guestSamples, outBytes, nil
}

// concSettleDelay is the fixed sleep after each concurrency level's teardown,
// longer than settleDelay because a level tears down up to L Firecracker
// processes at once and the next level must not overlap that cleanup
// (bench-v2-spec.md §6.5).
const concSettleDelay = 500 * time.Millisecond

// ConcLevelResult is one successful concurrency level's outcome: wall time to
// boot all L sandboxes, the per-sandbox create distribution (n = L, so gated
// percentiles are absent for small L — correct, not a bug), and derived
// throughput (bench-v2-spec.md §6.5, §9.4).
type ConcLevelResult struct {
	Level           int     `json:"level"`
	WallMs          float64 `json:"wall_ms"`
	PerSandbox      Stats   `json:"per_sandbox"`
	SandboxesPerSec float64 `json:"sandboxes_per_sec"`
}

// concLevelFailed is the JSON shape recorded when a create fails at a level:
// the level is marked failed with the error and the bench continues to the
// next level rather than aborting (bench-v2-spec.md §6.5).
type concLevelFailed struct {
	Level  int    `json:"level"`
	Failed bool   `json:"failed"`
	Error  string `json:"error"`
}

// runConcSweep boots L sandboxes concurrently for each level L in the sweep
// list on the first configured pack (bench-v2-spec.md §6.8), recording wall
// time to all-ready and per-sandbox create latency. Levels above 2× the host
// core count self-skip with a stderr note; a create failure at a level is
// recorded (not fatal) and the bench continues. It settles concSettleDelay
// between levels (bench-v2-spec.md §6.5).
func runConcSweep(rc *runContext) (ScenarioResult, error) {
	pack := rc.Config.Packs[0]
	cores := runtime.NumCPU()
	maxLevel := 2 * cores

	results := make([]any, 0, len(rc.Config.ConcLevels))
	var md strings.Builder
	md.WriteString("## Concurrency sweep\n\n")
	fmt.Fprintf(&md, "Boot L sandboxes concurrently on the `%s` pack, timing wall-clock to all-ready and per-sandbox create latency. %s settle between levels; levels above 2× host cores (%d) self-skip (bench-v2-spec.md §6.5).\n\n", pack, concSettleDelay, maxLevel)
	md.WriteString("| level | wall | create p50 | create p95 | create p99 | throughput |\n")
	md.WriteString("|---|---|---|---|---|---|\n")

	for _, level := range rc.Config.ConcLevels {
		if level < 1 {
			fmt.Fprintf(os.Stderr, "note: conc-sweep skipping non-positive level %d\n", level)
			continue
		}
		if level > maxLevel {
			fmt.Fprintf(os.Stderr, "note: conc-sweep skipping level %d (> 2× host cores = %d)\n", level, maxLevel)
			continue
		}

		levelRes, row := runConcLevel(rc, pack, level)
		results = append(results, levelRes)
		md.WriteString(row)

		time.Sleep(concSettleDelay)
	}

	return ScenarioResult{JSON: results, Markdown: md.String()}, nil
}

// runConcLevel boots level sandboxes concurrently from level goroutines,
// timing wall-clock to all-ready and each goroutine's own create latency. It
// always deletes whatever sandboxes were created (best effort). On any create
// failure it returns a failed-level record and a note to stderr rather than
// aborting the bench (bench-v2-spec.md §6.5). It returns the JSON value for
// this level and the rendered Markdown table row.
func runConcLevel(rc *runContext, pack string, level int) (any, string) {
	type createOutcome struct {
		createMs float64
		id       string
		err      error
	}
	outcomes := make([]createOutcome, level)

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < level; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			t0 := time.Now()
			id, err := rc.Client.Create(rc.Ctx, pack)
			outcomes[idx] = createOutcome{createMs: timeMs(t0), id: id, err: err}
		}(i)
	}
	wg.Wait()
	wallMs := timeMs(start)

	ids := make([]string, 0, level)
	samples := make([]float64, 0, level)
	var firstErr error
	for _, o := range outcomes {
		if o.err != nil {
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		ids = append(ids, o.id)
		samples = append(samples, o.createMs)
	}

	// Best-effort cleanup of every sandbox that did come up, even on a
	// partially-failed level, so a recorded failure never leaks VMs.
	for _, id := range ids {
		_ = rc.Client.Delete(rc.Ctx, id)
	}

	if firstErr != nil {
		fmt.Fprintf(os.Stderr, "note: conc-sweep level %d had a create failure: %v (recorded, continuing)\n", level, firstErr)
		row := fmt.Sprintf("| %d | — | — | — | — | failed: %v |\n", level, firstErr)
		return concLevelFailed{Level: level, Failed: true, Error: firstErr.Error()}, row
	}

	st := NewStats(samples)
	perSec := 0.0
	if wallMs > 0 {
		perSec = float64(level) / (wallMs / 1000.0)
	}
	res := ConcLevelResult{
		Level:           level,
		WallMs:          wallMs,
		PerSandbox:      st,
		SandboxesPerSec: perSec,
	}
	row := fmt.Sprintf("| %d | %s | %s | %s | %s | %.1f /s |\n",
		level, FormatMs(wallMs), FormatMs(st.P50Ms),
		FormatGatedMs(st.P95Ms), FormatGatedMs(st.P99Ms), perSec)
	return res, row
}

// poolSettleDelay is how long churn waits after its last cycle before
// snapshotting the firecracker process count. The daemon may run a warm pool
// that refills asynchronously, so the count can transiently differ from the
// baseline without a real leak; this delay lets any in-flight refill settle
// (bench-v2-spec.md §6.6, controller amendment for pool reality).
const poolSettleDelay = 2 * time.Second

// fdGrowthWarnThreshold is the daemon-fd growth above which churn flags a
// (non-fatal) leak warning: some growth can be legitimate, but it must be
// visible (bench-v2-spec.md §6.6).
const fdGrowthWarnThreshold = 8

// ChurnLeaks is the churn scenario's leak-detection summary: the before/after
// firecracker-process delta, the daemon-fd delta (nil when the daemon PID was
// ambiguous and the fd check was skipped), and the derived pass/warn/fail
// status (bench-v2-spec.md §6.6, §9.4).
type ChurnLeaks struct {
	FirecrackerProcsDelta int    `json:"firecracker_procs_delta"`
	DaemonFDDelta         *int   `json:"daemon_fd_delta"`
	Status                string `json:"status"`
}

// ChurnResult is the churn scenario's JSON payload (bench-v2-spec.md §9.4).
type ChurnResult struct {
	Cycle              Stats      `json:"cycle"`
	CyclesPerMin       float64    `json:"cycles_per_min"`
	FirstQuartileP50Ms float64    `json:"first_quartile_p50_ms"`
	LastQuartileP50Ms  float64    `json:"last_quartile_p50_ms"`
	DegradationWarning bool       `json:"degradation_warning"`
	Leaks              ChurnLeaks `json:"leaks"`
}

// leakStatus derives the churn leak-check verdict (bench-v2-spec.md §6.6): a
// nonzero firecracker-process delta is a hard fail (a VM outlived its delete);
// a daemon-fd growth beyond fdGrowthWarnThreshold is a non-fatal warning; every
// other case passes. fdChecked is false when the daemon PID could not be
// resolved to exactly one match, in which case fdDelta is ignored.
func leakStatus(fcDelta, fdDelta int, fdChecked bool) string {
	if fcDelta != 0 {
		return "fail"
	}
	if fdChecked && fdDelta > fdGrowthWarnThreshold {
		return "warn"
	}
	return "pass"
}

// firstLastQuartileP50 splits samples into a first-quartile and a
// last-quartile window BY CYCLE ORDER (not sorted — degradation is a trend
// over time, so the windows are the first and last quarter of the run) and
// returns each window's p50 plus whether the last-quartile p50 exceeds the
// first by more than 20% (bench-v2-spec.md §6.6). With fewer than 4 samples the
// windows would be empty, so it returns (0, 0, false).
func firstLastQuartileP50(samples []float64) (firstP50, lastP50 float64, warn bool) {
	n := len(samples)
	q := n / 4
	if q < 1 {
		return 0, 0, false
	}
	firstP50 = NewStats(samples[:q]).P50Ms
	lastP50 = NewStats(samples[n-q:]).P50Ms
	warn = firstP50 > 0 && lastP50 > firstP50*1.2
	return firstP50, lastP50, warn
}

// resolveDaemonPID resolves the single emberd daemon PID (`pgrep -x emberd`
// semantics). If there is not exactly one match the fd check is skipped — ok is
// false and callers must not read the daemon's fds — with a logged note
// explaining why (bench-v2-spec.md §6.6).
func resolveDaemonPID() (pid int, ok bool) {
	pids, err := findMatchingProcs(procRoot, daemonComm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: churn: cannot scan for the emberd daemon pid: %v — skipping fd check\n", err)
		return 0, false
	}
	if len(pids) != 1 {
		fmt.Fprintf(os.Stderr, "note: churn: expected exactly one emberd process, found %d %v — skipping fd check\n", len(pids), pids)
		return 0, false
	}
	return pids[0], true
}

// runChurn runs n back-to-back create → exec(hello) → delete cycles on the
// first configured pack (bench-v2-spec.md §6.8) with NO settle delay — the one
// documented exemption from §4's settle rule — timing each whole cycle. It
// brackets the run with a leak check (live firecracker process count, daemon fd
// count) and reports a first-vs-last-quartile degradation trend
// (bench-v2-spec.md §6.6).
func runChurn(rc *runContext) (ScenarioResult, error) {
	pack := rc.Config.Packs[0]
	hello := HelloPayload(pack)
	n := rc.Config.ChurnN
	if n < 1 {
		return ScenarioResult{}, fmt.Errorf("churn[%s]: -churn must be >= 1, got %d", pack, n)
	}

	// Leak baseline, sampled BEFORE the first cycle.
	fcBefore, err := countMatchingProcs(procRoot, firecrackerComm)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("churn[%s]: count firecracker procs (before): %w", pack, err)
	}
	daemonPID, fdChecked := resolveDaemonPID()
	fdBefore := 0
	if fdChecked {
		fdBefore, err = countFDs(procRoot, daemonPID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: churn: cannot read daemon fd count before the run (pid %d): %v — skipping fd check\n", daemonPID, err)
			fdChecked = false
		}
	}

	cycles := make([]float64, 0, n)
	runStart := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()

		id, err := rc.Client.Create(rc.Ctx, pack)
		if err != nil {
			return ScenarioResult{}, fmt.Errorf("churn[%s] cycle %d: create: %w", pack, i, err)
		}
		outcome, err := rc.Client.Exec(rc.Ctx, id, hello.Code, hello.Stdin, hello.TimeoutMs)
		if err != nil {
			_ = rc.Client.Delete(rc.Ctx, id) // best-effort cleanup; the exec error is what matters
			return ScenarioResult{}, fmt.Errorf("churn[%s] cycle %d: exec: %w", pack, i, err)
		}
		if err := checkPayload(hello, i, outcome); err != nil {
			_ = rc.Client.Delete(rc.Ctx, id) // best-effort cleanup; the mismatch is what matters
			return ScenarioResult{}, fmt.Errorf("churn[%s]: %w", pack, err)
		}
		if err := rc.Client.Delete(rc.Ctx, id); err != nil {
			return ScenarioResult{}, fmt.Errorf("churn[%s] cycle %d: delete: %w", pack, i, err)
		}

		// Deliberately NO settle delay here: churn measures back-to-back cycles,
		// the one exemption from §4's settle rule (bench-v2-spec.md §6.6).
		cycles = append(cycles, timeMs(t0))
	}
	wallMs := timeMs(runStart)

	// Post-run firecracker count, with the pool-reality amendment: sleep
	// poolSettleDelay first so an async warm-pool refill can finish; if the
	// delta is still nonzero, resample once after another poolSettleDelay and
	// trust that. Only a nonzero delta after the resample is a genuine leak.
	time.Sleep(poolSettleDelay)
	fcAfter, err := countMatchingProcs(procRoot, firecrackerComm)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("churn[%s]: count firecracker procs (after): %w", pack, err)
	}
	if fcAfter != fcBefore {
		time.Sleep(poolSettleDelay)
		fcAfter, err = countMatchingProcs(procRoot, firecrackerComm)
		if err != nil {
			return ScenarioResult{}, fmt.Errorf("churn[%s]: count firecracker procs (resample): %w", pack, err)
		}
	}
	fcDelta := fcAfter - fcBefore

	var fdDeltaPtr *int
	fdDelta := 0
	if fdChecked {
		fdAfter, err := countFDs(procRoot, daemonPID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: churn: cannot read daemon fd count after the run (pid %d): %v — skipping fd check\n", daemonPID, err)
			fdChecked = false
		} else {
			fdDelta = fdAfter - fdBefore
			fdDeltaPtr = &fdDelta
		}
	}

	status := leakStatus(fcDelta, fdDelta, fdChecked)
	firstP50, lastP50, degWarn := firstLastQuartileP50(cycles)
	cyclesPerMin := 0.0
	if wallMs > 0 {
		cyclesPerMin = float64(n) / (wallMs / 60000.0)
	}

	result := ChurnResult{
		Cycle:              NewStats(cycles),
		CyclesPerMin:       cyclesPerMin,
		FirstQuartileP50Ms: firstP50,
		LastQuartileP50Ms:  lastP50,
		DegradationWarning: degWarn,
		Leaks: ChurnLeaks{
			FirecrackerProcsDelta: fcDelta,
			DaemonFDDelta:         fdDeltaPtr,
			Status:                status,
		},
	}

	md := renderChurnMarkdown(pack, n, result, fdChecked)
	return ScenarioResult{JSON: result, Markdown: md}, nil
}

// renderChurnMarkdown renders the churn scenario's report section, printing the
// leak-check verdict prominently — a FAIL is called out in bold so a leaked
// firecracker process can't hide (bench-v2-spec.md §6.6).
func renderChurnMarkdown(pack string, n int, r ChurnResult, fdChecked bool) string {
	var md strings.Builder
	md.WriteString("## Churn (sustained create → exec → delete)\n\n")
	fmt.Fprintf(&md, "%d back-to-back cycles on the `%s` pack, no settle delay — the one exemption from the settle rule (bench-v2-spec.md §6.6). Per-cycle latency:\n\n", n, pack)
	md.WriteString(statsTable([]statsRow{{"cycle", r.Cycle}}))
	md.WriteString("\n")
	fmt.Fprintf(&md, "Throughput: %.1f cycles/min.\n\n", r.CyclesPerMin)

	fmt.Fprintf(&md, "Degradation (first vs last quartile p50): %s → %s", FormatMs(r.FirstQuartileP50Ms), FormatMs(r.LastQuartileP50Ms))
	if r.DegradationWarning {
		md.WriteString(" — **warning: last-quartile p50 is >20% above the first**")
	}
	md.WriteString(".\n\n")

	switch r.Leaks.Status {
	case "fail":
		md.WriteString("**Leak check: FAIL** — a firecracker process outlived its cycle's delete.\n\n")
	case "warn":
		md.WriteString("Leak check: **warn** (fd growth exceeded the threshold; visible, not fatal).\n\n")
	default:
		md.WriteString("Leak check: pass.\n\n")
	}
	fmt.Fprintf(&md, "- firecracker process delta: %+d (a nonzero delta is a failure)\n", r.Leaks.FirecrackerProcsDelta)
	if fdChecked && r.Leaks.DaemonFDDelta != nil {
		fmt.Fprintf(&md, "- daemon fd delta: %+d (warns above +%d)\n", *r.Leaks.DaemonFDDelta, fdGrowthWarnThreshold)
	} else {
		md.WriteString("- daemon fd delta: skipped (daemon PID could not be resolved unambiguously)\n")
	}
	md.WriteString("- workdir residue: skipped (Phase 1 daemon has no /info to report its work dir)\n")
	return md.String()
}

// MemoryResult is the memory scenario's JSON payload (bench-v2-spec.md §9.4).
// The idle_pss_mib / loaded_pss_mib fields reuse the Stats type, so their JSON
// keys read `_ms`; the values are MiB, as the Markdown report makes explicit.
type MemoryResult struct {
	IdlePSSMiB           Stats   `json:"idle_pss_mib"`
	LoadedPSSMiB         Stats   `json:"loaded_pss_mib"`
	EstSandboxesPer16GiB float64 `json:"est_sandboxes_per_16gib"`
}

// idleSettleDelay is how long the memory scenario lets the K sandboxes sit idle
// before reading their idle PSS (bench-v2-spec.md §6.7).
const idleSettleDelay = 2 * time.Second

// loadedSampleAt is when, into an in-flight mem-touch exec, the memory scenario
// samples a sandbox's loaded PSS: mem-touch has allocated and touched its 64
// MiB and is mid-sleep by the 1 s mark, so the sample catches the loaded
// footprint (bench-v2-spec.md §6.7).
const loadedSampleAt = 1 * time.Second

// findPayload returns pack's payload named name, and whether it exists. Unlike
// HelloPayload it does not panic on absence: the memory scenario needs
// mem-touch, which the shell pack's matrix omits, and that is a recoverable
// "fail this scenario clearly" condition rather than a programming error.
func lookupPayload(pack, name string) (Payload, bool) {
	for _, p := range PayloadsForPack(pack) {
		if p.Name == name {
			return p, true
		}
	}
	return Payload{}, false
}

// runMemory measures per-sandbox host memory on the first configured pack
// (bench-v2-spec.md §6.7, §6.8): boot K sandboxes sequentially — attributing
// each backing firecracker PID by its cmdline (robust under a warm pool) — let
// them idle, read each idle PSS from /proc/<pid>/smaps_rollup, then run
// mem-touch on each while a goroutine samples its loaded PSS at the 1 s mark.
// An unreadable smaps_rollup or an ambiguous PID attribution fails the scenario
// loudly rather than reporting a zero.
func runMemory(rc *runContext) (ScenarioResult, error) {
	pack := rc.Config.Packs[0]
	k := rc.Config.MemSandboxes
	if k < 1 {
		return ScenarioResult{}, fmt.Errorf("memory[%s]: -mem-sandboxes must be >= 1, got %d", pack, k)
	}
	memTouch, ok := lookupPayload(pack, "mem-touch")
	if !ok {
		return ScenarioResult{}, fmt.Errorf("memory[%s]: no mem-touch payload for this pack — the memory scenario needs it (bench-v2-spec.md §6.7)", pack)
	}

	type box struct {
		id  string
		pid int
	}
	boxes := make([]box, 0, k)
	cleanup := func() {
		for _, b := range boxes {
			_ = rc.Client.Delete(rc.Ctx, b.id)
		}
	}

	// Boot K sandboxes SEQUENTIALLY, attributing each backing PID right after
	// its create so attribution stays unambiguous (bench-v2-spec.md §6.7).
	for i := 0; i < k; i++ {
		id, err := rc.Client.Create(rc.Ctx, pack)
		if err != nil {
			cleanup()
			return ScenarioResult{}, fmt.Errorf("memory[%s] sandbox %d: create: %w", pack, i, err)
		}
		boxes = append(boxes, box{id: id})
		pid, err := attributeFirecrackerPID(procRoot, id)
		if err != nil {
			cleanup()
			return ScenarioResult{}, fmt.Errorf("memory[%s] sandbox %d (%s): attribute pid: %w", pack, i, id, err)
		}
		boxes[i].pid = pid
	}

	// Idle, then read idle PSS per sandbox.
	time.Sleep(idleSettleDelay)
	idle := make([]float64, 0, k)
	for i, b := range boxes {
		pss, err := readPSSMiB(smapsRollupPath(procRoot, b.pid))
		if err != nil {
			cleanup()
			return ScenarioResult{}, fmt.Errorf("memory[%s] sandbox %d (%s pid %d): idle PSS: %w", pack, i, b.id, b.pid, err)
		}
		idle = append(idle, pss)
	}

	// Loaded PSS: run mem-touch on each sandbox, sampling its PSS at the 1 s
	// mark while the exec is in flight.
	loaded := make([]float64, 0, k)
	for i, b := range boxes {
		pss, err := execAndSampleLoadedPSS(rc, pack, b.id, b.pid, memTouch)
		if err != nil {
			cleanup()
			return ScenarioResult{}, fmt.Errorf("memory[%s] sandbox %d (%s): %w", pack, i, b.id, err)
		}
		loaded = append(loaded, pss)
	}

	cleanup()

	loadedStats := NewStats(loaded)
	est := 0.0
	if loadedStats.P50Ms > 0 {
		est = 16384.0 / loadedStats.P50Ms
	}
	result := MemoryResult{
		IdlePSSMiB:           NewStats(idle),
		LoadedPSSMiB:         loadedStats,
		EstSandboxesPer16GiB: est,
	}

	md := renderMemoryMarkdown(pack, k, result)
	return ScenarioResult{JSON: result, Markdown: md}, nil
}

// execAndSampleLoadedPSS runs payload (mem-touch) on the sandbox while a
// goroutine samples the backing firecracker PSS at loadedSampleAt into the
// in-flight exec (bench-v2-spec.md §6.7). The exec is checked for correctness.
// If the exec returns before the 1 s sample fires — it should not, mem-touch
// sleeps 2 s — the sampler reports that as an error rather than returning a
// misleading PSS.
func execAndSampleLoadedPSS(rc *runContext, pack, id string, pid int, payload Payload) (float64, error) {
	type sample struct {
		pss float64
		err error
	}
	ch := make(chan sample, 1)
	stop := make(chan struct{})
	go func() {
		select {
		case <-time.After(loadedSampleAt):
			pss, err := readPSSMiB(smapsRollupPath(procRoot, pid))
			ch <- sample{pss: pss, err: err}
		case <-stop:
			ch <- sample{err: errors.New("mem-touch exec returned before the 1s loaded-PSS sample")}
		}
	}()

	outcome, err := rc.Client.Exec(rc.Ctx, id, payload.Code, payload.Stdin, payload.TimeoutMs)
	if err != nil {
		close(stop)
		<-ch
		return 0, fmt.Errorf("mem-touch exec: %w", err)
	}
	if err := checkPayload(payload, 0, outcome); err != nil {
		close(stop)
		<-ch
		return 0, err
	}

	s := <-ch
	if s.err != nil {
		return 0, fmt.Errorf("sample loaded PSS: %w", s.err)
	}
	return s.pss, nil
}

// formatMiB renders a MiB value to one decimal place. The memory scenario needs
// its own formatter because FormatMs would append " ms" to what is really a
// memory figure.
func formatMiB(v float64) string {
	return fmt.Sprintf("%.1f MiB", v)
}

// formatGatedMiB renders a gated percentile cell in MiB: "—" when the value was
// omitted for insufficient sample count, else formatMiB.
func formatGatedMiB(v *float64) string {
	if v == nil {
		return "—"
	}
	return formatMiB(*v)
}

// memStatsRow renders one MiB Stats row for the memory report table.
func memStatsRow(label string, s Stats) string {
	return fmt.Sprintf("| %s | %d | %s | %s | %s | %s | %s | %s |\n",
		label, s.N, formatMiB(s.MeanMs), formatMiB(s.P50Ms),
		formatGatedMiB(s.P95Ms), formatGatedMiB(s.P99Ms),
		formatMiB(s.MinMs), formatMiB(s.MaxMs))
}

// renderMemoryMarkdown renders the memory scenario's report section. It states
// that attribution is unambiguous only against a pool-disabled daemon
// (`./bin/emberd -pool-size=-1`), matching the note in the bench usage header
// (bench-v2-spec.md §6.7).
func renderMemoryMarkdown(pack string, k int, r MemoryResult) string {
	var md strings.Builder
	md.WriteString("## Memory (per-sandbox host PSS)\n\n")
	fmt.Fprintf(&md, "%d sandboxes on the `%s` pack, booted sequentially; PSS read from `/proc/<pid>/smaps_rollup` per sandbox. Values are MiB, not ms — the Stats block is reused (bench-v2-spec.md §6.7).\n\n", k, pack)
	md.WriteString("PID attribution is by the firecracker process cmdline (its per-sandbox api-socket path). It is unambiguous when run against a pool-disabled daemon (`./bin/emberd -pool-size=-1`); against a warm-pool daemon, unrelated refill VMs may make a create's attribution ambiguous, and the scenario then fails loudly rather than mis-attributing.\n\n")
	md.WriteString("| metric | n | mean | p50 | p95 | p99 | min | max |\n")
	md.WriteString("|---|---|---|---|---|---|---|---|\n")
	md.WriteString(memStatsRow("idle PSS", r.IdlePSSMiB))
	md.WriteString(memStatsRow("loaded PSS", r.LoadedPSSMiB))
	md.WriteString("\n")
	fmt.Fprintf(&md, "Illustrative estimate: ~%.0f sandboxes per 16 GiB (16384 ÷ loaded PSS p50 %s — a rough capacity figure, not a guarantee).\n\n", r.EstSandboxesPer16GiB, formatMiB(r.LoadedPSSMiB.P50Ms))
	return md.String()
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

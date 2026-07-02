// bench measures emberd sandbox lifecycle latencies against a live daemon.
//
// Usage:
//
//	./emberd &
//	go run ./bench [-addr 127.0.0.1:7777] [-run all] [-packs python] [flags...]
//
// Run `go run ./bench -h` for the full flag list (docs/bench-v2-spec.md §8).
// Progress goes to stderr; the Markdown report goes to stdout; JSON is
// written to bench/results/<timestamp>-<commit>.json and (overwriting) to
// bench/results.json.
//
// # Boot paths
//
// The Create latency the bench measures is entirely a function of how the
// daemon is configured — the bench itself just drives POST /sandboxes.
// There is no -boot-path flag: the label is read from the daemon's
// GET /info, falling back to "unknown (daemon has no /info endpoint)" until
// that endpoint lands (docs/bench-v2-spec.md §11.1). You still select the
// actual boot path by the daemon flags you start it with — run each mode
// against its own freshly-started daemon (the daemon flags live in
// cmd/emberd):
//
//	# 1. cold — no pool, no snapshot: every Create is a full ~400 ms cold boot.
//	#    A throwaway empty snapshot-dir guarantees no snapshot is registered at
//	#    start; -skip-warm keeps New() from building one. (A lazy background
//	#    build still kicks off after the first Create — see docs/benchmarks.md
//	#    for the caveat and how the numbers were captured cleanly.)
//	SNAP=$(mktemp -d)
//	./bin/emberd -pool-size=-1 -skip-warm -snapshot-dir="$SNAP" &
//	go run ./bench -run cold-boot
//
//	# 2. restore — pool disabled, snapshot warmed at start: every Create
//	#    restores directly from the template snapshot (~15–30 ms).
//	./bin/emberd -pool-size=-1 &
//	go run ./bench -run cold-boot
//
//	# 3. pool — default warm pool (size 3): every Create pops a pre-warmed VM
//	#    (guest-side <5 ms; wall time is dominated by HTTP + teardown).
//	./bin/emberd &
//	go run ./bench -run cold-boot
//
// # Memory scenario attribution
//
// The `memory` scenario attributes each sandbox to its backing firecracker
// process by that process's cmdline (its per-sandbox api-socket path). This is
// unambiguous only against a pool-disabled daemon (`./bin/emberd
// -pool-size=-1`): with a warm pool, unrelated refill VMs come and go, which
// can make a create's attribution ambiguous — the scenario then fails loudly
// rather than mis-attributing PSS to the wrong VM. Run `memory` against a
// pool-disabled daemon for clean numbers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// canonicalScenarios lists every known scenario name in the fixed order
// used both for "-run all" and for orchestration, regardless of the order
// given on the command line: churn always runs last because it stresses the
// daemon and nothing timed should follow it (docs/bench-v2-spec.md §8).
var canonicalScenarios = []string{
	"cold-boot", "ttfr", "exec", "workloads", "conc-sweep", "memory", "churn",
}

// runContext bundles everything a scenario implementation needs: the HTTP
// client, resolved flags, and a context for cancellation. Scenario
// implementations (bench/scenarios.go, later tasks) take a *runContext and
// return a ScenarioResult.
type runContext struct {
	Ctx    context.Context
	Client *Client
	Config Config
}

// scenarioFunc runs one scenario against a live daemon and returns its
// result for the report. Scenarios decide internally whether a failure is
// fatal; a returned error aborts the whole bench run.
type scenarioFunc func(rc *runContext) (ScenarioResult, error)

// scenarioRegistry maps canonical scenario names to their implementation.
// It starts empty in Phase 1a: later tasks populate it by calling
// registerScenario from an init() in bench/scenarios.go. A canonical name
// with no registered implementation is a known-but-unimplemented scenario —
// selectable, but skipped with a clear stderr note rather than silently
// ignored.
var scenarioRegistry = map[string]scenarioFunc{}

// registerScenario wires a scenario implementation into scenarioRegistry.
// It panics on an unknown name (a programming error, not a runtime one) and
// must only be called from package-level init().
func registerScenario(name string, fn scenarioFunc) {
	if !isCanonicalScenario(name) {
		panic("bench: registerScenario: unknown scenario " + name)
	}
	scenarioRegistry[name] = fn
}

func isCanonicalScenario(name string) bool {
	for _, n := range canonicalScenarios {
		if n == name {
			return true
		}
	}
	return false
}

// Config holds the resolved bench CLI flags (docs/bench-v2-spec.md §8).
type Config struct {
	Addr         string
	Run          string
	Packs        []string
	ColdN        int
	TTFRN        int
	ExecN        int
	WorkloadN    int
	ConcLevels   []int
	ChurnN       int
	MemSandboxes int
	OutDir       string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "emberd HTTP address")
	run := flag.String("run", "all", "comma-separated scenario names ("+strings.Join(canonicalScenarios, ", ")+") or \"all\"")
	packs := flag.String("packs", "python", "comma-separated language packs")
	cold := flag.Int("cold", 50, "iterations for cold-boot")
	ttfr := flag.Int("ttfr", 30, "iterations for ttfr")
	execN := flag.Int("exec", 200, "iterations for exec")
	workloadN := flag.Int("workload-n", 30, "iterations per payload in workloads")
	concLevels := flag.String("conc-levels", "1,2,4,8,16", "comma-separated concurrency sweep levels")
	churn := flag.Int("churn", 100, "cycles for churn")
	memSandboxes := flag.Int("mem-sandboxes", 8, "sandbox count for memory")
	out := flag.String("out", "bench/results", "directory for per-run JSON")
	flag.Parse()

	levels, err := parseConcLevels(*concLevels)
	if err != nil {
		fatalf("invalid -conc-levels: %v", err)
	}

	cfg := Config{
		Addr:         *addr,
		Run:          *run,
		Packs:        splitCSV(*packs),
		ColdN:        *cold,
		TTFRN:        *ttfr,
		ExecN:        *execN,
		WorkloadN:    *workloadN,
		ConcLevels:   levels,
		ChurnN:       *churn,
		MemSandboxes: *memSandboxes,
		OutDir:       *out,
	}

	selected, err := selectScenarios(cfg.Run)
	if err != nil {
		fatalf("%v", err)
	}

	WaitReady(cfg.Addr)
	client := NewClient(cfg.Addr)
	ctx := context.Background()

	prov := CaptureProvenance(execRunner{}, repoRoot(), time.Now(), flagsMap(cfg))
	env := CaptureEnv(ctx, client)
	report := NewReport(prov, env, binarySizes())

	rc := &runContext{Ctx: ctx, Client: client, Config: cfg}
	for _, name := range selected {
		fn, ok := scenarioRegistry[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "note: scenario %q not implemented yet — skipping\n", name)
			continue
		}
		fmt.Fprintf(os.Stderr, "running %s...\n", name)
		res, err := fn(rc)
		if err != nil {
			fatalf("%s: %v", name, err)
		}
		report.AddScenario(name, res)
	}

	report.WriteMarkdown(os.Stdout)
	if err := report.WriteFiles(cfg.OutDir); err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not write results: %v\n", err)
	}
}

// selectScenarios validates -run and returns the requested scenario names
// in canonical order. Run="all" returns every canonical scenario. An
// unknown name is fatal-by-error, listing the valid names.
func selectScenarios(run string) ([]string, error) {
	if strings.TrimSpace(run) == "all" {
		all := make([]string, len(canonicalScenarios))
		copy(all, canonicalScenarios)
		return all, nil
	}

	requested := make(map[string]bool)
	for _, n := range splitCSV(run) {
		if !isCanonicalScenario(n) {
			return nil, fmt.Errorf("unknown scenario %q — valid names: %s", n, strings.Join(canonicalScenarios, ", "))
		}
		requested[n] = true
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("no scenarios selected — valid names: %s", strings.Join(canonicalScenarios, ", "))
	}

	selected := make([]string, 0, len(requested))
	for _, n := range canonicalScenarios {
		if requested[n] {
			selected = append(selected, n)
		}
	}
	return selected, nil
}

// parseConcLevels parses a comma-separated list of positive integers, e.g.
// "1,2,4,8,16".
func parseConcLevels(s string) ([]int, error) {
	parts := splitCSV(s)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no levels given")
	}
	levels := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid level %q: %w", p, err)
		}
		levels = append(levels, n)
	}
	return levels, nil
}

// splitCSV splits s on commas, trims whitespace, and drops empty elements.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// flagsMap flattens Config into the JSON object recorded as
// Provenance.BenchFlags.
func flagsMap(cfg Config) map[string]any {
	return map[string]any{
		"addr":          cfg.Addr,
		"run":           cfg.Run,
		"packs":         cfg.Packs,
		"cold":          cfg.ColdN,
		"ttfr":          cfg.TTFRN,
		"exec":          cfg.ExecN,
		"workload-n":    cfg.WorkloadN,
		"conc-levels":   cfg.ConcLevels,
		"churn":         cfg.ChurnN,
		"mem-sandboxes": cfg.MemSandboxes,
		"out":           cfg.OutDir,
	}
}

// fatalf prints a formatted fatal error to stderr and exits 1.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fatal: "+format+"\n", args...)
	os.Exit(1)
}

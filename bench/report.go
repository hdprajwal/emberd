package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BinaryKB records built-binary sizes, unchanged from the pre-v2 harness.
type BinaryKB struct {
	Emberd     int `json:"emberd_kb"`
	EmberdInit int `json:"emberd_init_kb"`
}

// binarySizes reads bin/emberd and bin/emberd-init sizes relative to the
// repo root. Missing binaries report 0 rather than failing the run.
func binarySizes() BinaryKB {
	root := filepath.Join(repoRoot(), "bin")
	kb := func(name string) int {
		fi, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			return 0
		}
		return int(fi.Size() / 1024)
	}
	return BinaryKB{
		Emberd:     kb("emberd"),
		EmberdInit: kb("emberd-init"),
	}
}

// repoRoot walks up from the working directory to find the module root
// (identified by go.mod). Falls back to "." if none is found, e.g. when the
// bench binary is run outside the repo.
func repoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// ScenarioResult is what a scenario implementation hands back to the
// orchestrator in main.go: its JSON-serializable result (nested under
// scenarios.<name> in the output) and its already-rendered Markdown
// section. This is the seam later tasks plug into via scenarios.go — the
// orchestrator and report writer never need to know a scenario's internal
// result type.
type ScenarioResult struct {
	JSON     any
	Markdown string
}

// Report accumulates one full bench run: provenance, environment, binary
// sizes, and the scenario results that actually ran, in run order.
// Scenarios that were skipped or never selected are simply absent from the
// output (bench-v2-spec.md §9.4).
type Report struct {
	Provenance Provenance
	Env        Env
	BinaryKB   BinaryKB

	scenarios []namedScenarioResult // insertion order == the order scenarios ran
}

type namedScenarioResult struct {
	Name   string // canonical, hyphenated scenario name (e.g. "cold-boot")
	Result ScenarioResult
}

// NewReport starts a Report with no scenarios attached yet.
func NewReport(p Provenance, e Env, b BinaryKB) *Report {
	return &Report{Provenance: p, Env: e, BinaryKB: b}
}

// AddScenario attaches a completed scenario's result, keyed by its
// canonical name.
func (r *Report) AddScenario(name string, res ScenarioResult) {
	r.scenarios = append(r.scenarios, namedScenarioResult{Name: name, Result: res})
}

// scenarioJSONKey derives a scenario's JSON object key from its canonical
// (hyphenated) name, e.g. "conc-sweep" -> "conc_sweep", matching the schema
// in bench-v2-spec.md §9.4.
func scenarioJSONKey(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// MarshalJSON renders the top-level JSON schema from §9.4: provenance, env,
// binary_kb, and scenarios (present only for scenarios that ran).
func (r *Report) MarshalJSON() ([]byte, error) {
	scenarios := make(map[string]any, len(r.scenarios))
	for _, s := range r.scenarios {
		scenarios[scenarioJSONKey(s.Name)] = s.Result.JSON
	}
	out := struct {
		Provenance Provenance     `json:"provenance"`
		Env        Env            `json:"env"`
		BinaryKB   BinaryKB       `json:"binary_kb"`
		Scenarios  map[string]any `json:"scenarios,omitempty"`
	}{r.Provenance, r.Env, r.BinaryKB, scenarios}
	return json.Marshal(out)
}

// WriteMarkdown renders the human-readable report: an environment and
// provenance header, followed by one section per scenario that ran. The
// output is meant to be pasteable into docs/benchmarks.md with minimal
// editing.
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintf(w, "# emberd bench report\n\n")
	fmt.Fprintf(w, "Generated: %s  \nCommit: %s\n\n", r.Provenance.Timestamp, r.Provenance.GitCommit)

	fmt.Fprintf(w, "## Environment\n\n")
	fmt.Fprintf(w, "| | |\n|---|---|\n")
	fmt.Fprintf(w, "| OS | %s/%s |\n", r.Env.OS, r.Env.Arch)
	fmt.Fprintf(w, "| CPU | %s (%d cores) |\n", r.Env.CPUModel, r.Env.CPUCores)
	fmt.Fprintf(w, "| Host kernel | %s |\n", r.Env.HostKernel)
	fmt.Fprintf(w, "| CPU governor | %s |\n", r.Env.CPUGovernor)
	fmt.Fprintf(w, "| Firecracker | %s |\n", r.Env.FirecrackerVer)
	fmt.Fprintf(w, "| Guest RAM | %v |\n", r.Env.GuestRAMMiB)
	fmt.Fprintf(w, "| Guest vCPUs | %v |\n", r.Env.GuestVCPUs)
	fmt.Fprintf(w, "| Boot path | %s |\n", r.Env.BootPath)
	fmt.Fprintf(w, "| Daemon info source | %s |\n", r.Env.DaemonInfoSource)
	fmt.Fprintf(w, "| `emberd` binary | %d KB |\n", r.BinaryKB.Emberd)
	fmt.Fprintf(w, "| `emberd-init` binary | %d KB |\n\n", r.BinaryKB.EmberdInit)

	if r.Env.GovernorWarning {
		fmt.Fprintf(w, "> **Warning:** CPU governor is %q, not \"performance\" — results can swing 20-30%%.\n\n", r.Env.CPUGovernor)
	}

	fmt.Fprintf(w, "## Flags\n\n```\n")
	for _, k := range sortedKeys(r.Provenance.BenchFlags) {
		fmt.Fprintf(w, "-%s=%v\n", k, r.Provenance.BenchFlags[k])
	}
	fmt.Fprintf(w, "```\n\n")

	if len(r.scenarios) == 0 {
		fmt.Fprintf(w, "_No scenarios ran._\n")
		return
	}
	for _, s := range r.scenarios {
		fmt.Fprintf(w, "%s\n\n", strings.TrimRight(s.Result.Markdown, "\n"))
	}
}

// sortedKeys returns m's keys sorted lexically, for deterministic flag
// rendering.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// filesystemTimestamp converts an RFC 3339 timestamp into the filesystem-
// safe form used in per-run filenames, e.g. "2026-07-01T22:15:30Z" ->
// "20260701T221530Z".
func filesystemTimestamp(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "unknown-timestamp"
	}
	return t.UTC().Format("20060102T150405Z")
}

// WriteFiles writes the per-run archive file under outDir (relative paths
// resolve against the repo root) and overwrites the "latest" pointer at
// bench/results.json (bench-v2-spec.md §9.3), regardless of outDir.
func (r *Report) WriteFiles(outDir string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	root := repoRoot()
	dir := outDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, outDir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	name := fmt.Sprintf("%s-%s.json", filesystemTimestamp(r.Provenance.Timestamp), r.Provenance.GitCommit)
	perRun := filepath.Join(dir, name)
	if err := os.WriteFile(perRun, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", perRun, err)
	}
	fmt.Fprintf(os.Stderr, "results written to %s\n", perRun)

	latest := filepath.Join(root, "bench", "results.json")
	if err := os.WriteFile(latest, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", latest, err)
	}
	fmt.Fprintf(os.Stderr, "latest results written to %s\n", latest)
	return nil
}

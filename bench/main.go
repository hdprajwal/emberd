// bench measures emberd sandbox lifecycle latencies against a live daemon.
//
// Usage:
//
//	./emberd &
//	go run ./bench [-addr 127.0.0.1:7777] [-cold N] [-exec N] [-conc N] [-boot-path LABEL]
//
// Outputs a Markdown table to stdout and raw JSON to bench/results.json.
//
// # Boot paths
//
// The Create latency the bench measures is entirely a function of how the
// daemon is configured — the bench itself just drives POST /sandboxes. The
// -boot-path flag is only a label written into the JSON env block; you select
// the actual path by the daemon flags you start it with. Run each mode against
// its own freshly-started daemon (the daemon flags land in cmd/emberd):
//
//	# 1. cold — no pool, no snapshot: every Create is a full ~400 ms cold boot.
//	#    A throwaway empty snapshot-dir guarantees no snapshot is registered at
//	#    start; -skip-warm keeps New() from building one. (A lazy background
//	#    build still kicks off after the first Create — see docs/benchmarks.md
//	#    for the caveat and how the numbers were captured cleanly.)
//	SNAP=$(mktemp -d)
//	./bin/emberd -pool-size=-1 -skip-warm -snapshot-dir="$SNAP" &
//	go run ./bench -boot-path cold
//
//	# 2. restore — pool disabled, snapshot warmed at start: every Create
//	#    restores directly from the template snapshot (~15–30 ms).
//	./bin/emberd -pool-size=-1 &
//	go run ./bench -boot-path restore
//
//	# 3. pool — default warm pool (size 3): every Create pops a pre-warmed VM
//	#    (guest-side <5 ms; wall time is dominated by HTTP + teardown).
//	./bin/emberd &
//	go run ./bench -boot-path pool
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

var baseURL string

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "emberd HTTP address")
	coldN := flag.Int("cold", 15, "cold-boot iterations")
	execN := flag.Int("exec", 60, "exec iterations (single warm sandbox)")
	concN := flag.Int("conc", 8, "concurrent-create parallelism")
	bootPath := flag.String("boot-path", "cold", "boot-path label for the JSON env block: cold | restore | pool")
	flag.Parse()

	baseURL = "http://" + *addr

	waitReady(*addr)

	fmt.Fprintf(os.Stderr, "running cold-boot bench (n=%d)...\n", *coldN)
	cold := coldBootBench(*coldN)

	fmt.Fprintf(os.Stderr, "running exec bench (n=%d)...\n", *execN)
	execCreate, execAPI, execGuest := execBench(*execN)

	fmt.Fprintf(os.Stderr, "running concurrent-create bench (conc=%d)...\n", *concN)
	concWall, concSamples := concurrentCreateBench(*concN)

	binSizes := binarySizes()
	env := captureEnv(*bootPath)

	results := Results{
		Env:             env,
		BinaryKB:        binSizes,
		ColdBootMs:      stats(cold),
		ExecCreateMs:    stats([]time.Duration{execCreate}),
		ExecAPIMs:       stats(execAPI),
		ExecGuestMs:     stats(execGuest),
		ConcWallMs:      int(concWall.Milliseconds()),
		ConcSamples:     *concN,
		ConcPerSampleMs: stats(concSamples),
	}

	printTable(results)
	writeJSON(results)
}

// --- benchmark scenarios ---

func coldBootBench(n int) []time.Duration {
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		id := mustCreate("python")
		samples = append(samples, time.Since(t0))
		mustDelete(id)
	}
	return samples
}

func execBench(n int) (createLatency time.Duration, apiSamples, guestSamples []time.Duration) {
	t0 := time.Now()
	id := mustCreate("python")
	createLatency = time.Since(t0)

	apiSamples = make([]time.Duration, 0, n)
	guestSamples = make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t1 := time.Now()
		dur := mustExec(id, `print("hello world")`)
		apiSamples = append(apiSamples, time.Since(t1))
		guestSamples = append(guestSamples, dur)
	}

	mustDelete(id)
	return
}

func concurrentCreateBench(n int) (wall time.Duration, perSample []time.Duration) {
	ids := make([]string, n)
	samples := make([]time.Duration, n)
	var wg sync.WaitGroup
	wg.Add(n)

	t0 := time.Now()
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			ts := time.Now()
			ids[idx] = mustCreate("python")
			samples[idx] = time.Since(ts)
		}(i)
	}
	wg.Wait()
	wall = time.Since(t0)

	for _, id := range ids {
		mustDelete(id)
	}
	return wall, samples
}

// --- HTTP helpers ---

func mustCreate(pack string) string {
	body, _ := json.Marshal(map[string]string{"language_pack": pack})
	resp, err := http.Post(baseURL+"/sandboxes", "application/json", bytes.NewReader(body))
	if err != nil {
		fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		fatalf("create: unexpected status %d", resp.StatusCode)
	}
	var r struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		fatalf("create decode: %v", err)
	}
	return r.ID
}

func mustExec(id, code string) (guestDuration time.Duration) {
	body, _ := json.Marshal(map[string]any{"code": code, "timeout_ms": 5000})
	resp, err := http.Post(baseURL+"/sandboxes/"+id+"/exec", "application/json", bytes.NewReader(body))
	if err != nil {
		fatalf("exec: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fatalf("exec: unexpected status %d", resp.StatusCode)
	}
	var r struct {
		DurationMs int `json:"duration_ms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		fatalf("exec decode: %v", err)
	}
	return time.Duration(r.DurationMs) * time.Millisecond
}

func mustDelete(id string) {
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/sandboxes/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		fatalf("delete: unexpected status %d", resp.StatusCode)
	}
}

func waitReady(addr string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/sandboxes")
		if err == nil {
			resp.Body.Close()
			return
		}
		// 404 or 405 means the server is up, just no route — that's fine
		time.Sleep(200 * time.Millisecond)
	}
}

// --- stats ---

type Stats struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Min float64 `json:"min_ms"`
	Max float64 `json:"max_ms"`
}

func stats(samples []time.Duration) Stats {
	if len(samples) == 0 {
		return Stats{}
	}
	cp := make([]time.Duration, len(samples))
	copy(cp, samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })

	// Microsecond-derived so sub-millisecond samples (e.g. warm-pool-hit creates)
	// report an honest fractional value instead of truncating to 0 ms.
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	pct := func(p float64) float64 {
		idx := int(p/100*float64(len(cp)-1) + 0.5)
		if idx >= len(cp) {
			idx = len(cp) - 1
		}
		return ms(cp[idx])
	}
	return Stats{
		N:   len(cp),
		P50: pct(50),
		P95: pct(95),
		P99: pct(99),
		Min: ms(cp[0]),
		Max: ms(cp[len(cp)-1]),
	}
}

// --- environment capture ---

type Env struct {
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	CPUModel       string `json:"cpu_model"`
	CPUCores       int    `json:"cpu_cores"`
	FirecrackerVer string `json:"firecracker_version"`
	GuestRAMMiB    int    `json:"guest_ram_mib"`
	GuestVCPUs     int    `json:"guest_vcpus"`
	LanguagePack   string `json:"language_pack"`
	BootPath       string `json:"boot_path"`
}

func captureEnv(bootPath string) Env {
	return Env{
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CPUModel:       cpuModel(),
		CPUCores:       runtime.NumCPU(),
		FirecrackerVer: firecrackerVersion(),
		GuestRAMMiB:    256,
		GuestVCPUs:     1,
		LanguagePack:   "python (python3)",
		BootPath:       bootPathLabel(bootPath),
	}
}

// bootPathLabel expands the short -boot-path selector into the descriptive
// string recorded in the JSON env block. Unknown values pass through verbatim
// so the label is never silently wrong.
func bootPathLabel(mode string) string {
	switch mode {
	case "cold":
		return "cold boot (no snapshot, no pool)"
	case "restore":
		return "snapshot restore (pool disabled)"
	case "pool":
		return "warm pool hit (pre-warmed VM)"
	default:
		return mode
	}
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unknown"
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("model name")) {
			parts := bytes.SplitN(line, []byte(":"), 2)
			if len(parts) == 2 {
				return string(bytes.TrimSpace(parts[1]))
			}
		}
	}
	return "unknown"
}

func firecrackerVersion() string {
	bin := os.ExpandEnv("$HOME/.local/bin/firecracker")
	if p, err := exec.LookPath("firecracker"); err == nil {
		bin = p
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "unknown"
	}
	line := string(bytes.SplitN(bytes.TrimSpace(out), []byte("\n"), 2)[0])
	return line
}

// --- binary sizes ---

type BinaryKB struct {
	Emberd     int `json:"emberd_kb"`
	EmberdInit int `json:"emberd_init_kb"`
}

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

func repoRoot() string {
	// Walk up from bench/ to find go.mod
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

// --- output ---

type Results struct {
	Env             Env      `json:"env"`
	BinaryKB        BinaryKB `json:"binary_kb"`
	ColdBootMs      Stats    `json:"cold_boot_ms"`
	ExecCreateMs    Stats    `json:"exec_create_ms"`
	ExecAPIMs       Stats    `json:"exec_api_ms"`
	ExecGuestMs     Stats    `json:"exec_guest_ms"`
	ConcWallMs      int      `json:"conc_wall_ms"`
	ConcSamples     int      `json:"conc_samples"`
	ConcPerSampleMs Stats    `json:"conc_per_sample_ms"`
}

func printTable(r Results) {
	fmt.Printf("\n## Environment\n\n")
	fmt.Printf("| | |\n|---|---|\n")
	fmt.Printf("| OS | %s/%s |\n", r.Env.OS, r.Env.Arch)
	fmt.Printf("| CPU | %s (%d cores) |\n", r.Env.CPUModel, r.Env.CPUCores)
	fmt.Printf("| Firecracker | %s |\n", r.Env.FirecrackerVer)
	fmt.Printf("| Guest RAM | %d MiB |\n", r.Env.GuestRAMMiB)
	fmt.Printf("| Guest vCPUs | %d |\n", r.Env.GuestVCPUs)
	fmt.Printf("| Language pack | %s |\n", r.Env.LanguagePack)
	fmt.Printf("| Boot path | %s |\n\n", r.Env.BootPath)

	fmt.Printf("## Results\n\n")
	fmt.Printf("| Metric | P50 | P95 | P99 | Min | Max | N |\n")
	fmt.Printf("|---|---|---|---|---|---|---|\n")
	row := func(label string, s Stats) {
		fmt.Printf("| %s | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %d |\n",
			label, s.P50, s.P95, s.P99, s.Min, s.Max, s.N)
	}
	row("Cold boot (`POST /sandboxes`)", r.ColdBootMs)
	row("Exec API round-trip", r.ExecAPIMs)
	row("Exec guest-only (`DurationMs`)", r.ExecGuestMs)
	row("Concurrent create (per sandbox)", r.ConcPerSampleMs)

	fmt.Printf("\n| Concurrent create wall time (%d sandboxes) | %d ms |\n", r.ConcSamples, r.ConcWallMs)
	fmt.Printf("|---|---|\n")
	fmt.Printf("| `emberd` binary | %d KB |\n", r.BinaryKB.Emberd)
	fmt.Printf("| `emberd-init` binary | %d KB |\n", r.BinaryKB.EmberdInit)
}

func writeJSON(r Results) {
	data, _ := json.MarshalIndent(r, "", "  ")
	path := filepath.Join(repoRoot(), "bench", "results.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not write results.json: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "\nraw results written to bench/results.json\n")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fatal: "+format+"\n", args...)
	os.Exit(1)
}

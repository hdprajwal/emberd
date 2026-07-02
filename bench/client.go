package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hdprajwal/emberd/pkg/api"
)

// httpTimeout bounds every request the bench issues. Large-output execs are
// slow on purpose (bench-v2-spec.md §4), hence the generous ceiling.
const httpTimeout = 120 * time.Second

// readyTimeout is how long WaitReady waits before giving up.
const readyTimeout = 30 * time.Second

// Client is a thin wrapper around a shared http.Client bound to one emberd
// daemon address. All bench HTTP traffic goes through it so every request
// gets the same timeout and error handling. Helpers return errors rather
// than exiting; callers (scenarios) decide whether a failure is fatal.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client targeting the daemon at addr (host:port, no
// scheme).
func NewClient(addr string) *Client {
	return &Client{
		baseURL: "http://" + addr,
		http:    &http.Client{Timeout: httpTimeout},
	}
}

// ExecOutcome is the decoded result of one exec call, normalized so callers
// never have to care whether the daemon reported duration_us or duration_ms.
type ExecOutcome struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Error    string
	// GuestDurationMs is the guest-reported exec duration, converted to
	// float64 milliseconds regardless of source resolution.
	GuestDurationMs float64
	// GuestResolution is "us" when the daemon supplied a nonzero
	// duration_us (Phase 2), else "ms" — a Phase 1 daemon only ever sends
	// duration_ms, so this is always "ms" until bench-v2-spec.md §11.2
	// lands.
	GuestResolution string
}

// execResponse decodes both the Phase 1 wire shape (duration_ms only) and
// the Phase 2 addition (duration_us), without requiring pkg/api to have
// grown the new field yet. Once it does, the embedded field and this one
// agree and decoding is unaffected.
type execResponse struct {
	api.ExecResponse
	DurationUs int64 `json:"duration_us,omitempty"`
}

// Create posts a new sandbox using the given language pack and returns its
// ID.
func (c *Client) Create(ctx context.Context, pack string) (string, error) {
	body, err := json.Marshal(api.CreateSandboxRequest{LanguagePack: pack})
	if err != nil {
		return "", fmt.Errorf("create: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create: unexpected status %d", resp.StatusCode)
	}

	var r api.CreateSandboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("create: decode response: %w", err)
	}
	return r.ID, nil
}

// Exec runs code (with optional stdin and timeout) inside sandbox id and
// returns its outcome.
func (c *Client) Exec(ctx context.Context, id, code, stdin string, timeoutMs int) (ExecOutcome, error) {
	body, err := json.Marshal(api.ExecRequest{Code: code, Stdin: stdin, TimeoutMs: timeoutMs})
	if err != nil {
		return ExecOutcome{}, fmt.Errorf("exec: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sandboxes/"+id+"/exec", bytes.NewReader(body))
	if err != nil {
		return ExecOutcome{}, fmt.Errorf("exec: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return ExecOutcome{}, fmt.Errorf("exec: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ExecOutcome{}, fmt.Errorf("exec: unexpected status %d", resp.StatusCode)
	}

	var r execResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ExecOutcome{}, fmt.Errorf("exec: decode response: %w", err)
	}

	outcome := ExecOutcome{
		Stdout:   r.Stdout,
		Stderr:   r.Stderr,
		ExitCode: r.ExitCode,
		Error:    r.Error,
	}
	if r.DurationUs > 0 {
		outcome.GuestDurationMs = float64(r.DurationUs) / 1000.0
		outcome.GuestResolution = "us"
	} else {
		outcome.GuestDurationMs = float64(r.DurationMs)
		outcome.GuestResolution = "ms"
	}
	return outcome, nil
}

// Delete tears down sandbox id.
func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/sandboxes/"+id, nil)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// DaemonInfo mirrors the (future) GET /info response (bench-v2-spec.md
// §11.1). Phase 1 daemons don't expose this endpoint yet.
type DaemonInfo struct {
	GuestRAMMiB int      `json:"guest_ram_mib"`
	GuestVCPUs  int      `json:"guest_vcpus"`
	BootPath    string   `json:"boot_path"`
	Packs       []string `json:"packs"`
	WorkDir     string   `json:"work_dir"`
}

// Info fetches GET /info. ok is false when the daemon predates the endpoint
// (404) — callers must fall back to "unknown" and never hardcode guest
// facts.
func (c *Client) Info(ctx context.Context) (info DaemonInfo, ok bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/info", nil)
	if err != nil {
		return DaemonInfo{}, false, fmt.Errorf("info: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return DaemonInfo{}, false, fmt.Errorf("info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return DaemonInfo{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return DaemonInfo{}, false, fmt.Errorf("info: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return DaemonInfo{}, false, fmt.Errorf("info: decode response: %w", err)
	}
	return info, true, nil
}

// WaitReady polls addr until the daemon answers or readyTimeout elapses. On
// timeout it prints a clear fatal message and exits the process — it never
// falls through silently.
func WaitReady(addr string) {
	probe := &http.Client{Timeout: 2 * time.Second}
	url := "http://" + addr + "/sandboxes"
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		resp, err := probe.Get(url)
		if err == nil {
			resp.Body.Close()
			return // any response (incl. 404/405) means the daemon is up
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "fatal: daemon not reachable at %s after %s — is emberd running?\n", addr, readyTimeout)
	os.Exit(1)
}

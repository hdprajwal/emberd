package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hdprajwal/emberd/pkg/proto"
	"github.com/hdprajwal/emberd/pkg/sandbox"
)

// fakeManager is a minimal sandbox.Manager stand-in for HTTP handler tests. It
// returns a canned Info and a canned exec result, and is otherwise inert.
type fakeManager struct {
	info    sandbox.Info
	execRes proto.ExecResult
}

func (f *fakeManager) Create(ctx context.Context, languagePack string) (*sandbox.Sandbox, error) {
	return &sandbox.Sandbox{ID: "sb_test", LanguagePack: languagePack}, nil
}

func (f *fakeManager) Exec(ctx context.Context, id string, req proto.ExecRequest) (proto.ExecResult, error) {
	return f.execRes, nil
}

func (f *fakeManager) Delete(ctx context.Context, id string) error { return nil }

func (f *fakeManager) Info() sandbox.Info { return f.info }

func TestHandleExecPassesThroughDurations(t *testing.T) {
	cases := []struct {
		name       string
		result     proto.ExecResult
		wantUsWire bool // whether duration_us should appear in the JSON
	}{
		{
			name:       "guest reports microseconds",
			result:     proto.ExecResult{Stdout: "hi\n", ExitCode: 0, DurationMs: 12, DurationUs: 12847},
			wantUsWire: true,
		},
		{
			name:       "old guest omits microseconds",
			result:     proto.ExecResult{Stdout: "hi\n", ExitCode: 0, DurationMs: 12, DurationUs: 0},
			wantUsWire: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			NewServer(&fakeManager{execRes: tc.result}).Register(mux)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/sandboxes/sb_test/exec",
				strings.NewReader(`{"code":"print('hi')"}`))
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
			}

			var got ExecResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got.DurationMs != tc.result.DurationMs {
				t.Errorf("DurationMs = %d, want %d", got.DurationMs, tc.result.DurationMs)
			}
			if got.DurationUs != tc.result.DurationUs {
				t.Errorf("DurationUs = %d, want %d", got.DurationUs, tc.result.DurationUs)
			}

			// Guard the omitempty wire behavior so old clients see no new key.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode raw: %v", err)
			}
			if _, ok := raw["duration_us"]; ok != tc.wantUsWire {
				t.Errorf("duration_us present = %v, want %v; body = %s", ok, tc.wantUsWire, rec.Body.String())
			}
			if _, ok := raw["duration_ms"]; !ok {
				t.Errorf("duration_ms missing from body: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleInfo(t *testing.T) {
	want := sandbox.Info{
		GuestRAMMiB: 256,
		GuestVCPUs:  1,
		BootPath:    "warm-pool",
		Packs:       []string{"python", "shell"},
		WorkDir:     "/tmp/emberd",
	}

	mux := http.NewServeMux()
	NewServer(&fakeManager{info: want}).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/info", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Assert the exact wire keys the bench consumes (bench/env.go, spec §9.4).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	wantKeys := []string{"guest_ram_mib", "guest_vcpus", "boot_path", "packs", "work_dir"}
	for _, k := range wantKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("response missing key %q; body = %s", k, rec.Body.String())
		}
	}
	if len(raw) != len(wantKeys) {
		t.Errorf("response has %d keys, want %d; body = %s", len(raw), len(wantKeys), rec.Body.String())
	}

	// Round-trip the values to guard against field/tag drift.
	var got sandbox.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode into Info: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Info = %+v, want %+v", got, want)
	}
}

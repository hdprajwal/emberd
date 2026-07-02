package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hdprajwal/emberd/pkg/proto"
	"github.com/hdprajwal/emberd/pkg/sandbox"
)

// fakeManager is a minimal sandbox.Manager stand-in for HTTP handler tests. It
// returns a canned Info and is otherwise inert.
type fakeManager struct {
	info sandbox.Info
}

func (f *fakeManager) Create(ctx context.Context, languagePack string) (*sandbox.Sandbox, error) {
	return &sandbox.Sandbox{ID: "sb_test", LanguagePack: languagePack}, nil
}

func (f *fakeManager) Exec(ctx context.Context, id string, req proto.ExecRequest) (proto.ExecResult, error) {
	return proto.ExecResult{}, nil
}

func (f *fakeManager) Delete(ctx context.Context, id string) error { return nil }

func (f *fakeManager) Info() sandbox.Info { return f.info }

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

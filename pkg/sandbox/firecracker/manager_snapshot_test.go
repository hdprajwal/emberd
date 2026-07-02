//go:build integration

package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// TestColdBootExec exercises the real cold-boot path end to end: it boots a
// python microVM, runs a trivial exec, and tears it down. It also asserts the
// guest vsock UDS lives at "v.sock" inside the sandbox's own work dir, proving
// the relative-path + per-VM-cwd design that snapshot restore depends on.
//
// Requires KVM and the verification artifacts under ~/firecracker-verify/.
// Setup is deliberately local and minimal here; the shared helper arrives in a
// later task.
func TestColdBootExec(t *testing.T) {
	workDir := t.TempDir()
	m, err := New(Config{WorkDir: workDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	sb, err := m.Create(ctx, "python")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		delCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Delete(delCtx, sb.ID)
	})

	// The guest vsock socket must bind to "v.sock" inside this sandbox's own
	// dir — a relative path baked into snapshot state, unique per VM via cwd.
	sockPath := filepath.Join(workDir, sb.ID, "v.sock")
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("expected vsock socket at %s while VM is live: %v", sockPath, err)
	}

	res, err := m.Exec(ctx, sb.ID, proto.ExecRequest{Code: "print(6*7)"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "42\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "42\n")
	}

	if err := m.Delete(ctx, sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

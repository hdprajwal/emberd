package sandbox

import (
	"context"
	"errors"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// ErrNotFound is returned when a sandbox ID does not refer to a live sandbox.
var ErrNotFound = errors.New("sandbox not found")

// ErrUnknownPack is returned by Create when the requested language pack is not
// registered.
var ErrUnknownPack = errors.New("unknown language pack")

type Sandbox struct {
	ID           string
	LanguagePack string
}

// Info reports a Manager's resolved sandbox configuration: the guest machine
// shape every sandbox boots with, the fast-boot strategy a Create takes, the
// registered language packs, and the host work directory. It exposes daemon
// facts to observers such as the benchmark harness so recorded results carry
// real guest values instead of hardcoded guesses. The JSON tags are the wire
// contract for GET /info.
type Info struct {
	// GuestRAMMiB is the memory each microVM is given, in MiB.
	GuestRAMMiB int `json:"guest_ram_mib"`
	// GuestVCPUs is the number of vCPUs each microVM is given.
	GuestVCPUs int `json:"guest_vcpus"`
	// BootPath labels the fast-boot strategy a Create uses under the current
	// configuration (e.g. "warm-pool", "snapshot-restore", "cold-boot").
	BootPath string `json:"boot_path"`
	// Packs lists the registered language-pack names.
	Packs []string `json:"packs"`
	// WorkDir is the host directory holding per-sandbox runtime state.
	WorkDir string `json:"work_dir"`
}

type Manager interface {
	Create(ctx context.Context, languagePack string) (*Sandbox, error)
	Exec(ctx context.Context, id string, req proto.ExecRequest) (proto.ExecResult, error)
	Delete(ctx context.Context, id string) error
	// Info returns the resolved sandbox configuration for observability.
	Info() Info
}

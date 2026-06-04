package sandbox

import (
	"context"
	"errors"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// ErrNotFound is returned when a sandbox ID does not refer to a live sandbox.
var ErrNotFound = errors.New("sandbox not found")

type Sandbox struct {
	ID           string
	LanguagePack string
}

type Manager interface {
	Create(ctx context.Context, languagePack string) (*Sandbox, error)
	Exec(ctx context.Context, id string, req proto.ExecRequest) (proto.ExecResult, error)
	Delete(ctx context.Context, id string) error
}

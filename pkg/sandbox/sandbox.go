package sandbox

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a sandbox ID does not refer to a live sandbox.
var ErrNotFound = errors.New("sandbox not found")

// ErrExecNotImplemented is returned by Exec until the vsock control plane and
// the emberd-init guest agent are wired up.
var ErrExecNotImplemented = errors.New("exec not implemented")

type Sandbox struct {
	ID           string
	LanguagePack string
}

type Manager interface {
	Create(ctx context.Context, languagePack string) (*Sandbox, error)
	Exec(ctx context.Context, id, code string) (stdout, stderr string, exitCode int, err error)
	Delete(ctx context.Context, id string) error
}

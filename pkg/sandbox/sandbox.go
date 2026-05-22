package sandbox

import "context"

type Sandbox struct {
	ID           string
	LanguagePack string
}

type Manager interface {
	Create(ctx context.Context, languagePack string) (*Sandbox, error)
	Exec(ctx context.Context, id, code string) (stdout, stderr string, exitCode int, err error)
	Delete(ctx context.Context, id string) error
}

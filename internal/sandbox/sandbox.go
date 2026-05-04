package sandbox

import "context"

type ExecRequest struct {
	Image    string
	CloneURL string
	Branch   string
	Prompt   string
	EnvVars  map[string]string
	Binds    []string
	Timeout  int
	CPULimit string
	MemLimit string
}

type ExecResult struct {
	Output   string
	ExitCode int
	Error    string
}

type Runtime interface {
	Exec(ctx context.Context, req ExecRequest) (*ExecResult, error)
}

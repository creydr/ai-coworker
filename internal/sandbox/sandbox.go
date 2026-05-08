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
	Close() error
}

func PrepareEnvVars(req *ExecRequest) {
	if req.EnvVars == nil {
		req.EnvVars = make(map[string]string)
	}
	if req.CloneURL != "" {
		req.EnvVars["CLONE_URL"] = req.CloneURL
		if req.Branch != "" {
			req.EnvVars["CLONE_BRANCH"] = req.Branch
		}
	}
}

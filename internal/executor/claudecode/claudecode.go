package claudecode

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/sandbox"
)

type Executor struct {
	runtime         sandbox.Runtime
	image           string
	envVars         map[string]string
	timeout         int
	cpuLimit        string
	memLimit        string
	GitHubTokenFunc func(ctx context.Context, repo string) (string, error)
}

type Config struct {
	Runtime         sandbox.Runtime
	Image           string
	EnvVars         map[string]string
	TimeoutSeconds  int
	CPULimit        string
	MemoryLimit     string
	GitHubTokenFunc func(ctx context.Context, repo string) (string, error)
}

func New(cfg Config) *Executor {
	return &Executor{
		runtime:         cfg.Runtime,
		image:           cfg.Image,
		envVars:         cfg.EnvVars,
		timeout:         cfg.TimeoutSeconds,
		cpuLimit:        cfg.CPULimit,
		memLimit:        cfg.MemoryLimit,
		GitHubTokenFunc: cfg.GitHubTokenFunc,
	}
}

func (e *Executor) Execute(ctx context.Context, execCtx *executor.Context) (*executor.Result, error) {
	prompt := buildPrompt(execCtx)

	cloneURL := ""
	repo := ""
	if execCtx.Event != nil && execCtx.Event.Metadata != nil {
		repo = execCtx.Event.Metadata["repo"]
		if repo != "" {
			cloneURL = fmt.Sprintf("https://github.com/%s.git", repo)
		}
	}

	// Copy envVars so we don't mutate the shared map.
	envVars := make(map[string]string, len(e.envVars))
	for k, v := range e.envVars {
		envVars[k] = v
	}

	// If a GitHub token function is configured and we have a repo, fetch a token.
	if e.GitHubTokenFunc != nil && repo != "" {
		token, err := e.GitHubTokenFunc(ctx, repo)
		if err != nil {
			slog.Warn("failed to get GitHub token for sandbox", "repo", repo, "error", err)
		} else if token != "" {
			envVars["GITHUB_TOKEN"] = token
		}
	}

	req := sandbox.ExecRequest{
		Image:    e.image,
		CloneURL: cloneURL,
		Prompt:   prompt,
		EnvVars:  envVars,
		Timeout:  e.timeout,
		CPULimit: e.cpuLimit,
		MemLimit: e.memLimit,
	}

	result, err := e.runtime.Exec(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("sandbox execution failed: %w", err)
	}

	if result.ExitCode != 0 {
		return &executor.Result{
			Response: fmt.Sprintf("Execution failed (exit code %d): %s", result.ExitCode, result.Error),
		}, nil
	}

	return &executor.Result{
		Response: result.Output,
	}, nil
}

func buildPrompt(execCtx *executor.Context) string {
	var sb strings.Builder

	sb.WriteString("You are an AI coworker that helps with software development tasks. ")
	sb.WriteString("You have access to the repository and can make changes as needed.\n\n")

	if execCtx.Event != nil && execCtx.Event.Metadata != nil {
		if repo, ok := execCtx.Event.Metadata["repo"]; ok {
			sb.WriteString(fmt.Sprintf("Repository: %s\n", repo))
		}
		if issue, ok := execCtx.Event.Metadata["issue"]; ok {
			sb.WriteString(fmt.Sprintf("Issue: %s\n", issue))
		}
		sb.WriteString("\n")
	}

	if len(execCtx.Messages) > 0 {
		sb.WriteString("Conversation history:\n")
		for _, msg := range execCtx.Messages {
			role := "User"
			if msg.Role == domain.RoleAssistant {
				role = "Assistant"
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
		}
		sb.WriteString("\n")
	}

	if execCtx.Task != nil && execCtx.Task.Input != "" {
		sb.WriteString(fmt.Sprintf("Latest request: %s\n\n", execCtx.Task.Input))
	}

	sb.WriteString("If this task requires code changes, create a new branch and open a pull request with your changes.")

	return sb.String()
}

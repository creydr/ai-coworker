package claudecode

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/sandbox"
	"github.com/creydr/ai-coworker/internal/vcs"
)

// Executor runs tasks by invoking Claude Code inside a sandboxed container
type Executor struct {
	runtime     sandbox.Runtime
	image       string
	envVars     map[string]string
	binds       []string
	timeout     int
	cpuLimit    string
	memLimit    string
	skillImages []string
	vcsRegistry *vcs.Registry
}

// Config holds the configuration for creating a Claude Code executor
type Config struct {
	Runtime        sandbox.Runtime
	Image          string
	EnvVars        map[string]string
	Binds          []string
	TimeoutSeconds int
	CPULimit       string
	MemoryLimit    string
	SkillImages    []string
	VCSRegistry    *vcs.Registry
}

// New creates a new Claude Code executor from the given configuration
func New(cfg Config) *Executor {
	return &Executor{
		runtime:     cfg.Runtime,
		image:       cfg.Image,
		envVars:     cfg.EnvVars,
		binds:       cfg.Binds,
		timeout:     cfg.TimeoutSeconds,
		cpuLimit:    cfg.CPULimit,
		memLimit:    cfg.MemoryLimit,
		skillImages: cfg.SkillImages,
		vcsRegistry: cfg.VCSRegistry,
	}
}

func (e *Executor) Execute(ctx context.Context, execCtx *executor.Context) (*executor.Result, error) {
	prompt := buildPrompt(execCtx)
	primaryProvider, repo, branch, allMatches := e.resolveVCSContext(execCtx)

	cloneURL := ""
	if primaryProvider != nil && repo != "" {
		cloneURL = primaryProvider.CloneURL(repo)
	}

	envVars := e.collectVCSTokens(ctx, primaryProvider, repo, allMatches)

	req := sandbox.ExecRequest{
		Image:       e.image,
		CloneURL:    cloneURL,
		Branch:      branch,
		Prompt:      prompt,
		EnvVars:     envVars,
		Binds:       e.binds,
		Timeout:     e.timeout,
		CPULimit:    e.cpuLimit,
		MemLimit:    e.memLimit,
		SkillImages: e.skillImages,
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

// resolveVCSContext determines the primary VCS provider and repository using
// two tiers: (1) event metadata from a VCS adapter, (2) repo URLs extracted
// from message content — enabling non-VCS adapters (e.g. Slack) to trigger
// code tasks when the user pastes a repo URL.
func (e *Executor) resolveVCSContext(execCtx *executor.Context) (vcs.Provider, string, string, []vcs.RepoMatch) {
	var primaryProvider vcs.Provider
	var repo, branch string

	// Tier 1: event came from a VCS adapter — provider is known.
	if execCtx.Event != nil && execCtx.Event.Metadata != nil {
		repo = execCtx.Event.Metadata["repo"]
		branch = execCtx.Event.Metadata["pr_branch"]

		if repo != "" {
			if sourceVCS := execCtx.Event.Metadata["vcs"]; sourceVCS != "" && e.vcsRegistry != nil {
				primaryProvider, _ = e.vcsRegistry.ByName(sourceVCS)
			}
		}
	}

	// Tier 2: extract repo URLs from message content, thread history, and metadata.
	var allMatches []vcs.RepoMatch
	if e.vcsRegistry != nil {
		if execCtx.Task != nil {
			allMatches = append(allMatches, e.vcsRegistry.ExtractReposFromText(execCtx.Task.Input)...)
		}
		for _, msg := range execCtx.Messages {
			allMatches = append(allMatches, e.vcsRegistry.ExtractReposFromText(msg.Content)...)
		}
		if execCtx.Event != nil && execCtx.Event.Metadata != nil {
			for _, v := range execCtx.Event.Metadata {
				allMatches = append(allMatches, e.vcsRegistry.ExtractReposFromText(v)...)
			}
		}
		if repo == "" && len(allMatches) > 0 {
			repo = allMatches[0].Repo
			primaryProvider = allMatches[0].Provider
		}
	}

	return primaryProvider, repo, branch, allMatches
}

// collectVCSTokens copies base env vars and adds scoped tokens for all
// involved VCS providers so the sandbox can access repos across multiple
// platforms in a single session.
func (e *Executor) collectVCSTokens(ctx context.Context, primaryProvider vcs.Provider, repo string, allMatches []vcs.RepoMatch) map[string]string {
	// Copy envVars so we don't mutate the shared map.
	envVars := make(map[string]string, len(e.envVars))
	for k, v := range e.envVars {
		envVars[k] = v
	}

	if e.vcsRegistry == nil {
		return envVars
	}

	seen := map[string]bool{}
	var credURLs []string

	if primaryProvider != nil && repo != "" {
		token, err := primaryProvider.CreateTokenForRepo(ctx, repo)
		if err != nil {
			slog.Warn("failed to get VCS token for sandbox", "provider", primaryProvider.Name(), "repo", repo, "error", err)
		} else if token != "" {
			envVars[primaryProvider.TokenEnvVar()] = token
			credURLs = append(credURLs, primaryProvider.CredentialURL(token))
			seen[primaryProvider.Name()] = true
		}
	}

	for _, match := range allMatches {
		if seen[match.Provider.Name()] {
			continue
		}
		seen[match.Provider.Name()] = true
		token, err := match.Provider.CreateTokenForRepo(ctx, match.Repo)
		if err != nil {
			slog.Warn("failed to get VCS token for sandbox", "provider", match.Provider.Name(), "repo", match.Repo, "error", err)
			continue
		}
		if token != "" {
			envVars[match.Provider.TokenEnvVar()] = token
			credURLs = append(credURLs, match.Provider.CredentialURL(token))
		}
	}

	if len(credURLs) > 0 {
		envVars["VCS_CREDENTIAL_URLS"] = strings.Join(credURLs, "\n")
	}

	return envVars
}

func buildPrompt(execCtx *executor.Context) string {
	var sb strings.Builder

	sb.WriteString("You are an AI coworker that helps with software development tasks.\n")
	sb.WriteString("You have access to the repository and can make changes as needed.\n")
	sb.WriteString("Use the `gh` CLI to fetch issue or PR details (e.g. `gh issue view <number>`) and to check CI status (e.g. `gh run list`, `gh run view <id> --log-failed`).\n\n")

	isPR := false
	if execCtx.Event != nil && execCtx.Event.Metadata != nil {
		if repo, ok := execCtx.Event.Metadata["repo"]; ok {
			fmt.Fprintf(&sb, "Repository: %s\n", repo)
		}
		if num, ok := execCtx.Event.Metadata["issue_num"]; ok {
			if execCtx.Event.Metadata["is_pr"] == "true" {
				fmt.Fprintf(&sb, "Pull Request: #%s\n", num)
				isPR = true
			} else {
				fmt.Fprintf(&sb, "Issue: #%s\n", num)
			}
		}
		sb.WriteString("\n")

		if path := execCtx.Event.Metadata["path"]; path != "" {
			fmt.Fprintf(&sb, "File: %s", path)
			if start, end := execCtx.Event.Metadata["start_line"], execCtx.Event.Metadata["line"]; start != "" && end != "" {
				fmt.Fprintf(&sb, " (lines %s-%s)", start, end)
			} else if end := execCtx.Event.Metadata["line"]; end != "" {
				fmt.Fprintf(&sb, " (line %s)", end)
			}
			sb.WriteString("\n\n")
		}

		if thread := execCtx.Event.Metadata["comment_thread"]; thread != "" {
			sb.WriteString("=== YOUR TASK ===\n")
			if quoted := execCtx.Event.Metadata["quoted_text"]; quoted != "" {
				fmt.Fprintf(&sb, "Comment on: %q\n", quoted)
			}
			sb.WriteString(thread)
			sb.WriteString("\n")
		}

		if docCtx := execCtx.Event.Metadata["document_context"]; docCtx != "" {
			sb.WriteString(docCtx)
			sb.WriteString("\n\n")
		}
	}

	if len(execCtx.Messages) > 0 {
		sb.WriteString("Conversation history:\n")
		for _, msg := range execCtx.Messages {
			role := "User"
			if msg.Role == domain.RoleAssistant {
				role = "Assistant"
			}
			fmt.Fprintf(&sb, "%s: %s\n", role, msg.Content)
		}
		sb.WriteString("\n")
	}

	if execCtx.Task != nil && execCtx.Task.Input != "" {
		fmt.Fprintf(&sb, "Latest request: %s\n\n", execCtx.Task.Input)
	}

	if isPR {
		sb.WriteString("You are on the PR branch. Make changes and push directly to this branch.")
	} else {
		sb.WriteString("If this task requires code changes, create a new branch and open a pull request with your changes.")
	}

	return sb.String()
}

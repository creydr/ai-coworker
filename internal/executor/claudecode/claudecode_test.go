package claudecode

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/sandbox"
)

// mockRuntime records the ExecRequest it received and returns a canned ExecResult.
type mockRuntime struct {
	request sandbox.ExecRequest
	result  *sandbox.ExecResult
	err     error
}

func (m *mockRuntime) Exec(_ context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	m.request = req
	return m.result, m.err
}

func newTestContext() *executor.Context {
	return &executor.Context{
		Thread: &domain.Thread{
			ID:     "thread-1",
			Status: domain.ThreadActive,
		},
		Messages: []domain.Message{
			{ID: "msg-1", Role: domain.RoleUser, Content: "Please fix the bug"},
			{ID: "msg-2", Role: domain.RoleAssistant, Content: "I will look into it"},
		},
		Task: &domain.Task{
			ID:    "task-1",
			Input: "Fix the nil pointer in handler.go",
		},
		Event: &domain.IncomingEvent{
			Metadata: map[string]string{
				"repo":      "octocat/hello-world",
				"issue_num": "42",
			},
		},
	}
}

func TestExecute_Success(t *testing.T) {
	mock := &mockRuntime{
		result: &sandbox.ExecResult{
			Output:   "Changes applied successfully",
			ExitCode: 0,
		},
	}

	e := New(Config{
		Runtime:        mock,
		Image:          "ghcr.io/ai-coworker:latest",
		EnvVars:        map[string]string{"ANTHROPIC_API_KEY": "sk-test-key"},
		TimeoutSeconds: 300,
		CPULimit:       "2",
		MemoryLimit:    "4g",
	})

	execCtx := newTestContext()
	result, err := e.Execute(context.Background(), execCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the sandbox was called with the correct image.
	if mock.request.Image != "ghcr.io/ai-coworker:latest" {
		t.Errorf("expected image %q, got %q", "ghcr.io/ai-coworker:latest", mock.request.Image)
	}

	// Verify CloneURL was constructed correctly.
	expectedCloneURL := "https://github.com/octocat/hello-world.git"
	if mock.request.CloneURL != expectedCloneURL {
		t.Errorf("expected CloneURL %q, got %q", expectedCloneURL, mock.request.CloneURL)
	}

	// Verify the prompt contains the repo name and task input.
	if !strings.Contains(mock.request.Prompt, "octocat/hello-world") {
		t.Errorf("prompt should contain repo name, got: %s", mock.request.Prompt)
	}
	if !strings.Contains(mock.request.Prompt, "Fix the nil pointer in handler.go") {
		t.Errorf("prompt should contain task input, got: %s", mock.request.Prompt)
	}

	// Verify ANTHROPIC_API_KEY was passed in env vars.
	if v, ok := mock.request.EnvVars["ANTHROPIC_API_KEY"]; !ok || v != "sk-test-key" {
		t.Errorf("expected ANTHROPIC_API_KEY=sk-test-key in env vars, got: %v", mock.request.EnvVars)
	}

	// Verify the response matches the sandbox output.
	if result.Response != "Changes applied successfully" {
		t.Errorf("expected response %q, got %q", "Changes applied successfully", result.Response)
	}
}

func TestExecute_WithGitHubTokenFunc(t *testing.T) {
	mock := &mockRuntime{
		result: &sandbox.ExecResult{
			Output:   "done",
			ExitCode: 0,
		},
	}

	e := New(Config{
		Runtime:        mock,
		Image:          "test-image",
		EnvVars:        map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		TimeoutSeconds: 60,
		GitHubTokenFunc: func(_ context.Context, repo string) (string, error) {
			return "ghs_test_token_123", nil
		},
	})

	execCtx := newTestContext()
	_, err := e.Execute(context.Background(), execCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify GITHUB_TOKEN appears in the env vars passed to the sandbox.
	if v, ok := mock.request.EnvVars["GITHUB_TOKEN"]; !ok || v != "ghs_test_token_123" {
		t.Errorf("expected GITHUB_TOKEN=ghs_test_token_123 in env vars, got: %v", mock.request.EnvVars)
	}

	// Also verify ANTHROPIC_API_KEY is still present.
	if v, ok := mock.request.EnvVars["ANTHROPIC_API_KEY"]; !ok || v != "sk-test" {
		t.Errorf("expected ANTHROPIC_API_KEY=sk-test in env vars, got: %v", mock.request.EnvVars)
	}
}

func TestExecute_GitHubTokenFuncError(t *testing.T) {
	mock := &mockRuntime{
		result: &sandbox.ExecResult{
			Output:   "completed despite token error",
			ExitCode: 0,
		},
	}

	e := New(Config{
		Runtime:        mock,
		Image:          "test-image",
		EnvVars:        map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		TimeoutSeconds: 60,
		GitHubTokenFunc: func(_ context.Context, repo string) (string, error) {
			return "", fmt.Errorf("token service unavailable")
		},
	})

	execCtx := newTestContext()
	result, err := e.Execute(context.Background(), execCtx)
	if err != nil {
		t.Fatalf("execution should proceed despite token error, got: %v", err)
	}

	// Verify GITHUB_TOKEN is NOT in env vars.
	if _, ok := mock.request.EnvVars["GITHUB_TOKEN"]; ok {
		t.Errorf("GITHUB_TOKEN should not be present when token func returns error, got: %v", mock.request.EnvVars)
	}

	// Verify execution still completed.
	if result.Response != "completed despite token error" {
		t.Errorf("expected response %q, got %q", "completed despite token error", result.Response)
	}
}

func TestExecute_NonZeroExitCode(t *testing.T) {
	mock := &mockRuntime{
		result: &sandbox.ExecResult{
			Output:   "",
			ExitCode: 1,
			Error:    "container OOM killed",
		},
	}

	e := New(Config{
		Runtime:        mock,
		Image:          "test-image",
		EnvVars:        map[string]string{},
		TimeoutSeconds: 60,
	})

	execCtx := newTestContext()
	result, err := e.Execute(context.Background(), execCtx)
	if err != nil {
		t.Fatalf("non-zero exit code should not return an error, got: %v", err)
	}

	// Verify the result contains the error message.
	if !strings.Contains(result.Response, "exit code 1") {
		t.Errorf("response should contain exit code, got: %s", result.Response)
	}
	if !strings.Contains(result.Response, "container OOM killed") {
		t.Errorf("response should contain error message, got: %s", result.Response)
	}
}

func TestExecute_PRBranch(t *testing.T) {
	mock := &mockRuntime{
		result: &sandbox.ExecResult{
			Output:   "pushed to branch",
			ExitCode: 0,
		},
	}

	e := New(Config{
		Runtime:        mock,
		Image:          "test-image",
		EnvVars:        map[string]string{},
		TimeoutSeconds: 60,
	})

	execCtx := &executor.Context{
		Thread: &domain.Thread{ID: "t-1"},
		Task:   &domain.Task{ID: "task-1", Input: "fix the test"},
		Event: &domain.IncomingEvent{
			Metadata: map[string]string{
				"repo":      "org/repo",
				"is_pr":     "true",
				"pr_branch": "feat/my-feature",
				"issue_num": "7",
			},
		},
	}

	_, err := e.Execute(context.Background(), execCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.request.Branch != "feat/my-feature" {
		t.Errorf("Branch = %q, want %q", mock.request.Branch, "feat/my-feature")
	}
	if mock.request.CloneURL != "https://github.com/org/repo.git" {
		t.Errorf("CloneURL = %q, want %q", mock.request.CloneURL, "https://github.com/org/repo.git")
	}
}

func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name    string
		execCtx *executor.Context
		want    []string
		wantNot []string
	}{
		{
			name: "full context with all fields",
			execCtx: &executor.Context{
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: "Hello"},
					{Role: domain.RoleAssistant, Content: "Hi there"},
				},
				Task: &domain.Task{Input: "Implement feature X"},
				Event: &domain.IncomingEvent{
					Metadata: map[string]string{
						"repo":      "org/repo",
						"issue_num": "99",
					},
				},
			},
			want: []string{
				"Repository: org/repo",
				"Issue: #99",
				"User: Hello",
				"Assistant: Hi there",
				"Latest request: Implement feature X",
				"pull request",
			},
		},
		{
			name: "no event metadata",
			execCtx: &executor.Context{
				Task: &domain.Task{Input: "Do something"},
			},
			want: []string{
				"Latest request: Do something",
				"AI coworker",
			},
			wantNot: []string{
				"Repository:",
				"Issue:",
				"Conversation history:",
			},
		},
		{
			name: "empty task input",
			execCtx: &executor.Context{
				Task: &domain.Task{Input: ""},
				Event: &domain.IncomingEvent{
					Metadata: map[string]string{
						"repo": "myorg/myrepo",
					},
				},
			},
			want: []string{
				"Repository: myorg/myrepo",
			},
			wantNot: []string{
				"Latest request:",
			},
		},
		{
			name: "nil event",
			execCtx: &executor.Context{
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: "question?"},
				},
			},
			want: []string{
				"User: question?",
				"Conversation history:",
			},
			wantNot: []string{
				"Repository:",
			},
		},
		{
			name: "nil task",
			execCtx: &executor.Context{
				Event: &domain.IncomingEvent{
					Metadata: map[string]string{
						"repo": "a/b",
					},
				},
			},
			want: []string{
				"Repository: a/b",
			},
			wantNot: []string{
				"Latest request:",
			},
		},
		{
			name:    "completely empty context",
			execCtx: &executor.Context{},
			want: []string{
				"AI coworker",
				"create a new branch",
			},
			wantNot: []string{
				"Repository:",
				"Latest request:",
				"Conversation history:",
			},
		},
		{
			name: "PR context shows PR branch instructions",
			execCtx: &executor.Context{
				Task: &domain.Task{Input: "fix this"},
				Event: &domain.IncomingEvent{
					Metadata: map[string]string{
						"repo":      "org/repo",
						"issue_num": "5",
						"is_pr":     "true",
					},
				},
			},
			want: []string{
				"Pull Request: #5",
				"on the PR branch",
			},
			wantNot: []string{
				"Issue: #5",
				"create a new branch",
			},
		},
		{
			name: "issue context shows create branch instructions",
			execCtx: &executor.Context{
				Task: &domain.Task{Input: "fix this"},
				Event: &domain.IncomingEvent{
					Metadata: map[string]string{
						"repo":      "org/repo",
						"issue_num": "10",
					},
				},
			},
			want: []string{
				"Issue: #10",
				"create a new branch",
			},
			wantNot: []string{
				"Pull Request:",
				"on the PR branch",
			},
		},
		{
			name: "gh CLI hint is present",
			execCtx: &executor.Context{
				Task: &domain.Task{Input: "test"},
			},
			want: []string{
				"gh",
				"CLI",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildPrompt(tt.execCtx)

			for _, s := range tt.want {
				if !strings.Contains(prompt, s) {
					t.Errorf("expected prompt to contain %q, got:\n%s", s, prompt)
				}
			}
			for _, s := range tt.wantNot {
				if strings.Contains(prompt, s) {
					t.Errorf("expected prompt NOT to contain %q, got:\n%s", s, prompt)
				}
			}
		})
	}
}

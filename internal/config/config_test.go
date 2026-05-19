package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

const minConfig = "database:\n  url: postgres://localhost/test\nllm:\n  provider: claude\n  apiKey: sk-test\n  model: claude-sonnet-4-6\nsandbox:\n  image: quay.io/test/sandbox:latest\n"

func TestLoad_Defaults(t *testing.T) {
	path := writeTempConfig(t, minConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Workers != 4 {
		t.Errorf("Workers = %d, want 4", cfg.Workers)
	}
	if cfg.Sandbox.TimeoutSeconds != 600 {
		t.Errorf("Sandbox.TimeoutSeconds = %d, want 600", cfg.Sandbox.TimeoutSeconds)
	}
	if cfg.LLM.Vertex.Region != "global" {
		t.Errorf("LLM.Vertex.Region = %q, want %q", cfg.LLM.Vertex.Region, "global")
	}
	if cfg.Sandbox.Runtime != "docker" {
		t.Errorf("Sandbox.Runtime = %q, want %q", cfg.Sandbox.Runtime, "docker")
	}
}

func TestLoad_SandboxRuntimeValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:   "docker runtime is valid",
			config: minConfig + "  runtime: docker\n",
		},
		{
			name:   "kubernetes runtime with namespace is valid",
			config: minConfig + "  runtime: kubernetes\n  namespace: ai-coworker\n",
		},
		{
			name:    "kubernetes runtime without namespace is invalid",
			config:  minConfig + "  runtime: kubernetes\n",
			wantErr: "sandbox.namespace is required",
		},
		{
			name:    "unknown runtime is invalid",
			config:  minConfig + "  runtime: podman\n",
			wantErr: "sandbox.runtime must be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.config)
			_, err := Load(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoad_EnvVarOverrides(t *testing.T) {
	path := writeTempConfig(t, minConfig)

	t.Setenv("AI_COWORKER__LLM__API_KEY", "sk-test-key")
	t.Setenv("AI_COWORKER__LLM__PROVIDER", "claude")
	t.Setenv("AI_COWORKER__GITHUB__ENABLED", "true")
	t.Setenv("AI_COWORKER__GITHUB__APP_ID", "12345")
	t.Setenv("AI_COWORKER__GITHUB__PRIVATE_KEY", "fake-pem")
	t.Setenv("AI_COWORKER__GITHUB__WEBHOOK_SECRET", "secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.APIKey != "sk-test-key" {
		t.Errorf("LLM.APIKey = %q, want %q", cfg.LLM.APIKey, "sk-test-key")
	}
	if cfg.LLM.Provider != "claude" {
		t.Errorf("LLM.Provider = %q, want %q", cfg.LLM.Provider, "claude")
	}
	if !cfg.GitHub.Enabled {
		t.Error("GitHub.Enabled = false, want true")
	}
	if cfg.GitHub.AppID != 12345 {
		t.Errorf("GitHub.AppID = %d, want 12345", cfg.GitHub.AppID)
	}
}

func TestLoad_NestedEnvVars(t *testing.T) {
	path := writeTempConfig(t, minConfig)

	t.Setenv("AI_COWORKER__LLM__PROVIDER", "vertex")
	t.Setenv("AI_COWORKER__LLM__VERTEX__PROJECT_ID", "my-project")
	t.Setenv("AI_COWORKER__LLM__VERTEX__REGION", "us-east1")
	t.Setenv("AI_COWORKER__LLM__OPENAI__BASE_URL", "https://api.example.com/v1")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.Vertex.ProjectID != "my-project" {
		t.Errorf("LLM.Vertex.ProjectID = %q, want %q", cfg.LLM.Vertex.ProjectID, "my-project")
	}
	if cfg.LLM.Vertex.Region != "us-east1" {
		t.Errorf("LLM.Vertex.Region = %q, want %q", cfg.LLM.Vertex.Region, "us-east1")
	}
	if cfg.LLM.OpenAI.BaseURL != "https://api.example.com/v1" {
		t.Errorf("LLM.OpenAI.BaseURL = %q, want %q", cfg.LLM.OpenAI.BaseURL, "https://api.example.com/v1")
	}
}

func TestLoad_EnvVarOverridesConfigFile(t *testing.T) {
	path := writeTempConfig(t, "llm:\n  provider: claude\n  apiKey: sk-test\n  model: old-model\ndatabase:\n  url: postgres://localhost/test\nsandbox:\n  image: quay.io/test/sandbox:latest\n")

	t.Setenv("AI_COWORKER__LLM__MODEL", "new-model")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.Provider != "claude" {
		t.Errorf("LLM.Provider = %q, want %q (from file)", cfg.LLM.Provider, "claude")
	}
	if cfg.LLM.Model != "new-model" {
		t.Errorf("LLM.Model = %q, want %q (from env)", cfg.LLM.Model, "new-model")
	}
}

func TestLoad_MissingSandboxImage(t *testing.T) {
	config := "database:\n  url: postgres://localhost/test\nllm:\n  provider: claude\n  apiKey: sk-test\n  model: claude-sonnet-4-6\n"
	path := writeTempConfig(t, config)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing sandbox.image, got nil")
	}
	if !strings.Contains(err.Error(), "sandbox.image is required") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "sandbox.image is required")
	}
}

func TestLoad_UnknownProvider(t *testing.T) {
	config := "database:\n  url: postgres://localhost/test\nllm:\n  provider: gemini\n  apiKey: sk-test\n  model: some-model\nsandbox:\n  image: quay.io/test/sandbox:latest\n"
	path := writeTempConfig(t, config)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "llm.provider must be") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "llm.provider must be")
	}
}

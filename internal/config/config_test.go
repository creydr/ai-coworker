package config

import (
	"os"
	"path/filepath"
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

func TestLoad_Defaults(t *testing.T) {
	path := writeTempConfig(t, "database:\n  url: postgres://localhost/test\n")
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
}

func TestLoad_EnvVarOverrides(t *testing.T) {
	path := writeTempConfig(t, "database:\n  url: postgres://localhost/test\n")

	t.Setenv("AI_COWORKER__LLM__API_KEY", "sk-test-key")
	t.Setenv("AI_COWORKER__LLM__PROVIDER", "claude")
	t.Setenv("AI_COWORKER__GITHUB__ENABLED", "true")
	t.Setenv("AI_COWORKER__GITHUB__APP_ID", "12345")

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
	path := writeTempConfig(t, "database:\n  url: postgres://localhost/test\n")

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
	path := writeTempConfig(t, "llm:\n  provider: claude\n  model: old-model\ndatabase:\n  url: postgres://localhost/test\n")

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

package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Database DatabaseConfig `koanf:"database"`
	LLM      LLMConfig      `koanf:"llm"`
	Slack    SlackConfig    `koanf:"slack"`
	GitHub   GitHubConfig   `koanf:"github"`
	Sandbox  SandboxConfig  `koanf:"sandbox"`
	Workers  int            `koanf:"workers"`
}

type DatabaseConfig struct {
	URL string `koanf:"url"`
}

type LLMConfig struct {
	Provider string       `koanf:"provider"`
	APIKey   string       `koanf:"api_key"`
	Model    string       `koanf:"model"`
	Vertex   VertexConfig `koanf:"vertex"`
	OpenAI   OpenAIConfig `koanf:"openai"`
}

type OpenAIConfig struct {
	BaseURL string `koanf:"base_url"`
}

type VertexConfig struct {
	ProjectID string `koanf:"project_id"`
	Region    string `koanf:"region"`
}

type SlackConfig struct {
	Enabled  bool   `koanf:"enabled"`
	AppToken string `koanf:"app_token"`
	BotToken string `koanf:"bot_token"`
}

type GitHubConfig struct {
	Enabled       bool   `koanf:"enabled"`
	AppID         int64  `koanf:"app_id"`
	PrivateKey    string `koanf:"private_key"`
	WebhookSecret string `koanf:"webhook_secret"`
	BotUsername   string `koanf:"bot_username"`
}

type SandboxConfig struct {
	Runtime        string `koanf:"runtime"`
	Image          string `koanf:"image"`
	TimeoutSeconds int    `koanf:"timeout_seconds"`
	CPULimit       string `koanf:"cpu_limit"`
	MemoryLimit    string `koanf:"memory_limit"`
	Namespace      string `koanf:"namespace"`
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("loading config from %s: %w", path, err)
	}

	if err := k.Load(env.Provider("AI_COWORKER__", ".", func(s string) string {
		key := strings.TrimPrefix(s, "AI_COWORKER__")
		key = strings.ToLower(strings.ReplaceAll(key, "__", "."))
		return key
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env config: %w", err)
	}

	cfg := &Config{}
	if err := k.Unmarshal("", cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	// Apply defaults
	if cfg.Workers == 0 {
		cfg.Workers = 4
	}
	if cfg.Sandbox.TimeoutSeconds == 0 {
		cfg.Sandbox.TimeoutSeconds = 600
	}
	if cfg.LLM.Vertex.Region == "" {
		cfg.LLM.Vertex.Region = "global"
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.Database.URL == "" {
		return fmt.Errorf("database.url is required")
	}
	if c.LLM.Provider == "" {
		return fmt.Errorf("llm.provider is required")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	switch c.LLM.Provider {
	case "claude":
		if c.LLM.APIKey == "" {
			return fmt.Errorf("llm.api_key is required for claude provider")
		}
	case "vertex":
		if c.LLM.Vertex.ProjectID == "" {
			return fmt.Errorf("llm.vertex.project_id is required for vertex provider")
		}
	case "openai":
		if c.LLM.OpenAI.BaseURL == "" {
			return fmt.Errorf("llm.openai.base_url is required for openai provider")
		}
	}
	if c.GitHub.Enabled {
		if c.GitHub.AppID == 0 {
			return fmt.Errorf("github.app_id is required when github is enabled")
		}
		if c.GitHub.PrivateKey == "" {
			return fmt.Errorf("github.private_key is required when github is enabled")
		}
		if c.GitHub.WebhookSecret == "" {
			return fmt.Errorf("github.webhook_secret is required when github is enabled")
		}
	}
	if c.Slack.Enabled {
		if c.Slack.AppToken == "" {
			return fmt.Errorf("slack.app_token is required when slack is enabled")
		}
		if c.Slack.BotToken == "" {
			return fmt.Errorf("slack.bot_token is required when slack is enabled")
		}
	}
	return nil
}

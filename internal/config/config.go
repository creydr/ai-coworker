package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const (
	// defaultListenAddr is the default address the HTTP server listens on.
	defaultListenAddr = ":8080"
	// defaultWorkers is the default number of concurrent task workers.
	defaultWorkers = 4
	// defaultSandboxTimeoutSeconds is the default sandbox execution timeout in seconds.
	defaultSandboxTimeoutSeconds = 600
	// defaultVertexRegion is the default Google Cloud region for Vertex AI.
	defaultVertexRegion = "global"
	// defaultSandboxRuntime is the default sandbox runtime.
	defaultSandboxRuntime = RuntimeDocker

	// RuntimeDocker is the Docker sandbox runtime.
	RuntimeDocker = "docker"
	// RuntimeKubernetes is the Kubernetes sandbox runtime.
	RuntimeKubernetes = "kubernetes"
)

type Config struct {
	Database   DatabaseConfig   `koanf:"database"`
	LLM        LLMConfig        `koanf:"llm"`
	Slack      SlackConfig      `koanf:"slack"`
	GitHub     GitHubConfig     `koanf:"github"`
	GoogleDocs GoogleDocsConfig `koanf:"googledocs"`
	Sandbox    SandboxConfig    `koanf:"sandbox"`
	Workers    int              `koanf:"workers"`
}

type DatabaseConfig struct {
	URL string `koanf:"url"`
}

type LLMConfig struct {
	Provider string       `koanf:"provider"`
	APIKey   string       `koanf:"apiKey"`
	Model    string       `koanf:"model"`
	Vertex   VertexConfig `koanf:"vertex"`
	OpenAI   OpenAIConfig `koanf:"openai"`
}

type OpenAIConfig struct {
	BaseURL string `koanf:"baseUrl"`
}

type VertexConfig struct {
	ProjectID string `koanf:"projectId"`
	Region    string `koanf:"region"`
}

type SlackConfig struct {
	Enabled  bool   `koanf:"enabled"`
	AppToken string `koanf:"appToken"`
	BotToken string `koanf:"botToken"`
}

type GitHubConfig struct {
	Enabled       bool     `koanf:"enabled"`
	AppID         int64    `koanf:"appId"`
	PrivateKey    string   `koanf:"privateKey"`
	WebhookSecret string   `koanf:"webhookSecret"`
	BotUsername   string   `koanf:"botUsername"`
	AllowedUsers  []string `koanf:"allowedUsers"`
	ListenAddr    string   `koanf:"listenAddr"`
}

type GoogleDocsConfig struct {
	Enabled                bool   `koanf:"enabled"`
	ServiceAccountKeyPath  string `koanf:"serviceAccountKeyPath"`
	ListenAddr             string `koanf:"listenAddr"`
	WebhookURL             string `koanf:"webhookUrl"`
	DocumentContentMaxSize string `koanf:"documentContentMaxSize"`
}

type SandboxConfig struct {
	Runtime        string   `koanf:"runtime"`
	Image          string   `koanf:"image"`
	TimeoutSeconds int      `koanf:"timeoutSeconds"`
	CPULimit       string   `koanf:"cpuLimit"`
	MemoryLimit    string   `koanf:"memoryLimit"`
	Namespace      string   `koanf:"namespace"`
	ServiceAccount string   `koanf:"serviceAccount"`
	SkillImages    []string `koanf:"skillImages"`
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("loading config from %s: %w", path, err)
	}

	if err := k.Load(env.Provider("AI_COWORKER__", ".", func(s string) string {
		key := strings.TrimPrefix(s, "AI_COWORKER__")
		parts := strings.Split(key, "__")
		for i, p := range parts {
			parts[i] = snakeToCamel(p)
		}
		return strings.Join(parts, ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env config: %w", err)
	}

	cfg := &Config{}
	if err := k.Unmarshal("", cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	// Apply defaults
	if cfg.GitHub.ListenAddr == "" {
		cfg.GitHub.ListenAddr = defaultListenAddr
	}
	if cfg.Workers == 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.Sandbox.TimeoutSeconds == 0 {
		cfg.Sandbox.TimeoutSeconds = defaultSandboxTimeoutSeconds
	}
	if cfg.LLM.Vertex.Region == "" {
		cfg.LLM.Vertex.Region = defaultVertexRegion
	}
	if cfg.Sandbox.Runtime == "" {
		cfg.Sandbox.Runtime = defaultSandboxRuntime
	}
	if cfg.GoogleDocs.ListenAddr == "" {
		cfg.GoogleDocs.ListenAddr = ":8082"
	}
	if cfg.GoogleDocs.DocumentContentMaxSize == "" {
		cfg.GoogleDocs.DocumentContentMaxSize = "100KB"
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func snakeToCamel(s string) string {
	s = strings.ToLower(s)
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
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
			return fmt.Errorf("llm.apiKey is required for claude provider")
		}
	case "vertex":
		if c.LLM.Vertex.ProjectID == "" {
			return fmt.Errorf("llm.vertex.projectId is required for vertex provider")
		}
	case "openai":
		if c.LLM.OpenAI.BaseURL == "" {
			return fmt.Errorf("llm.openai.baseUrl is required for openai provider")
		}
	}
	switch c.Sandbox.Runtime {
	case RuntimeDocker, RuntimeKubernetes:
	default:
		return fmt.Errorf("sandbox.runtime must be %q or %q, got %q", RuntimeDocker, RuntimeKubernetes, c.Sandbox.Runtime)
	}
	if c.Sandbox.Runtime == RuntimeKubernetes && c.Sandbox.Namespace == "" {
		return fmt.Errorf("sandbox.namespace is required when sandbox.runtime is 'kubernetes'")
	}
	if c.GitHub.Enabled {
		if c.GitHub.AppID == 0 {
			return fmt.Errorf("github.appId is required when github is enabled")
		}
		if c.GitHub.PrivateKey == "" {
			return fmt.Errorf("github.privateKey is required when github is enabled")
		}
		if c.GitHub.WebhookSecret == "" {
			return fmt.Errorf("github.webhookSecret is required when github is enabled")
		}
	}
	if c.GoogleDocs.Enabled {
		if c.GoogleDocs.ServiceAccountKeyPath == "" {
			return fmt.Errorf("googledocs.serviceAccountKeyPath is required when googledocs is enabled")
		}
		if c.GoogleDocs.WebhookURL == "" {
			return fmt.Errorf("googledocs.webhookUrl is required when googledocs is enabled")
		}
	}
	if c.Slack.Enabled {
		if c.Slack.AppToken == "" {
			return fmt.Errorf("slack.appToken is required when slack is enabled")
		}
		if c.Slack.BotToken == "" {
			return fmt.Errorf("slack.botToken is required when slack is enabled")
		}
	}
	return nil
}

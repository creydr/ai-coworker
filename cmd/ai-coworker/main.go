package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/adapter/github"
	"github.com/creydr/ai-coworker/internal/adapter/googledocs"
	"github.com/creydr/ai-coworker/internal/adapter/slack"
	"github.com/creydr/ai-coworker/internal/config"
	"github.com/creydr/ai-coworker/internal/engine"
	"github.com/creydr/ai-coworker/internal/executor/claudecode"
	"github.com/creydr/ai-coworker/internal/executor/llmexec"
	"github.com/creydr/ai-coworker/internal/llm"
	llmanthropic "github.com/creydr/ai-coworker/internal/llm/anthropic"
	llmopenai "github.com/creydr/ai-coworker/internal/llm/openai"
	"github.com/creydr/ai-coworker/internal/sandbox"
	"github.com/creydr/ai-coworker/internal/sandbox/docker"
	k8ssandbox "github.com/creydr/ai-coworker/internal/sandbox/kubernetes"
	"github.com/creydr/ai-coworker/internal/store"
	"github.com/creydr/ai-coworker/internal/vcs"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// 2. Set up signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 3. Create PostgresStore, connect, migrate, defer close.
	db, err := store.NewPostgresStore(ctx, cfg.Database.URL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// 4. Create LLM provider.
	var llmProvider llm.Provider
	switch cfg.LLM.Provider {
	case "vertex":
		llmProvider = llmanthropic.NewVertex(ctx, cfg.LLM.Vertex.ProjectID, cfg.LLM.Vertex.Region, cfg.LLM.Model)
	case "openai":
		llmProvider = llmopenai.New(cfg.LLM.OpenAI.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	default:
		llmProvider = llmanthropic.New(cfg.LLM.APIKey, cfg.LLM.Model)
	}

	// 5. Create Router with the store.
	router := engine.NewRouter(db)

	// 6. If Slack is enabled: create adapter, register, start in goroutine.
	if cfg.Slack.Enabled {
		slackAdapter := slack.New(cfg.Slack.AppToken, cfg.Slack.BotToken)
		router.RegisterAdapter(slackAdapter)
		go func() {
			if err := slackAdapter.Start(ctx, router.HandleEvent); err != nil {
				slog.Error("slack adapter stopped", "error", err)
			}
		}()
		slog.Info("slack adapter enabled")
	}

	// 7. If GitHub is enabled: create adapter, register, start in goroutine.
	if cfg.GitHub.Enabled {
		githubAdapter, err := github.New(cfg.GitHub.AppID, []byte(cfg.GitHub.PrivateKey), cfg.GitHub.WebhookSecret, cfg.GitHub.BotUsername, cfg.GitHub.ListenAddr, cfg.GitHub.APIBaseURL, cfg.GitHub.AllowedUsers)
		if err != nil {
			slog.Error("failed to create github adapter", "error", err)
			os.Exit(1)
		}
		router.RegisterAdapter(githubAdapter)
		go func() {
			if err := githubAdapter.Start(ctx, router.HandleEvent); err != nil {
				slog.Error("github adapter stopped", "error", err)
			}
		}()
		slog.Info("github adapter enabled", "app_id", cfg.GitHub.AppID, "private_key_len", len(cfg.GitHub.PrivateKey))
	}

	// 8. If Google Docs is enabled: create adapter, register, start in goroutine.
	if cfg.GoogleDocs.Enabled {
		gdocsAdapter, err := googledocs.New(googledocs.Config{
			ServiceAccountKeyPath:  cfg.GoogleDocs.ServiceAccountKeyPath,
			ListenAddr:             cfg.GoogleDocs.ListenAddr,
			WebhookURL:             cfg.GoogleDocs.WebhookURL,
			DocumentContentMaxSize: cfg.GoogleDocs.DocumentContentMaxSize,
			MaxPaginationPages:     cfg.GoogleDocs.MaxPaginationPages,
			Store:                  db,
		})
		if err != nil {
			slog.Error("failed to create googledocs adapter", "error", err)
			os.Exit(1)
		}
		router.RegisterAdapter(gdocsAdapter)
		go func() {
			if err := gdocsAdapter.Start(ctx, router.HandleEvent); err != nil {
				slog.Error("googledocs adapter stopped", "error", err)
			}
		}()
		slog.Info("googledocs adapter enabled")
	}

	// 9. Create sandbox runtime.
	var sandboxRuntime sandbox.Runtime
	switch cfg.Sandbox.Runtime {
	case config.RuntimeKubernetes:
		sandboxRuntime, err = k8ssandbox.New(cfg.Sandbox.Namespace, cfg.Sandbox.ServiceAccount)
		if err != nil {
			slog.Error("failed to create kubernetes sandbox runtime", "error", err)
			os.Exit(1)
		}
		slog.Info("sandbox runtime: kubernetes", "namespace", cfg.Sandbox.Namespace)
	default:
		sandboxRuntime, err = docker.New()
		if err != nil {
			slog.Error("failed to create docker sandbox runtime", "error", err)
			os.Exit(1)
		}
		slog.Info("sandbox runtime: docker")
	}
	defer sandboxRuntime.Close()

	// 9. Create Claude Code executor with sandbox runtime.
	vcsRegistry := vcs.NewRegistry()
	for _, a := range router.Adapters() {
		if va, ok := a.(adapter.VCSAware); ok {
			vcsRegistry.Register(va.VCSProvider())
		}
	}
	sandboxEnv := map[string]string{}
	switch cfg.LLM.Provider {
	case "vertex":
		sandboxEnv["CLAUDE_CODE_USE_VERTEX"] = "1"
		sandboxEnv["ANTHROPIC_VERTEX_PROJECT_ID"] = cfg.LLM.Vertex.ProjectID
		sandboxEnv["ANTHROPIC_VERTEX_REGION"] = cfg.LLM.Vertex.Region
		adcPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		if adcPath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				slog.Error("failed to determine home directory for ADC credentials", "error", err)
				os.Exit(1)
			}
			adcPath = home + "/.config/gcloud/application_default_credentials.json"
		}
		adcContent, err := os.ReadFile(adcPath)
		if err != nil {
			slog.Error("failed to read ADC credentials file", "path", adcPath, "error", err)
			os.Exit(1)
		}
		sandboxEnv["GOOGLE_APPLICATION_CREDENTIALS_JSON"] = string(adcContent)
	case "openai":
		if cfg.LLM.APIKey != "" {
			sandboxEnv["OPENAI_API_KEY"] = cfg.LLM.APIKey
		}
		if cfg.LLM.OpenAI.BaseURL != "" {
			sandboxEnv["OPENAI_BASE_URL"] = cfg.LLM.OpenAI.BaseURL
		}
	default:
		sandboxEnv["ANTHROPIC_API_KEY"] = cfg.LLM.APIKey
	}
	codeExec := claudecode.New(claudecode.Config{
		Runtime:        sandboxRuntime,
		Image:          cfg.Sandbox.Image,
		EnvVars:        sandboxEnv,
		TimeoutSeconds: cfg.Sandbox.TimeoutSeconds,
		CPULimit:       cfg.Sandbox.CPULimit,
		MemoryLimit:    cfg.Sandbox.MemoryLimit,
		SkillImages:    cfg.Sandbox.SkillImages,
		VCSRegistry:    vcsRegistry,
	})

	// 10. Create LLM executor with the provider.
	llmExec := llmexec.New(llmProvider)

	// 11. Create IntentClassifier with the provider.
	classifier := engine.NewIntentClassifier(llmProvider)

	// 12. Create WorkerPool with all components.
	pool := engine.NewWorkerPool(db, router, classifier, codeExec, llmExec, cfg.Workers)

	// 13. Start worker pool.
	pool.Start(ctx)

	// 14. Log startup, wait for shutdown signal.
	slog.Info("ai-coworker started", "workers", cfg.Workers, "llm_provider", cfg.LLM.Provider, "llm_model", cfg.LLM.Model)
	<-ctx.Done()
	slog.Info("ai-coworker shutting down")
	pool.Wait()
}

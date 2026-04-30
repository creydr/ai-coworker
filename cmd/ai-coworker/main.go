package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/creydr/ai-coworker/internal/adapter/github"
	"github.com/creydr/ai-coworker/internal/adapter/slack"
	"github.com/creydr/ai-coworker/internal/config"
	"github.com/creydr/ai-coworker/internal/engine"
	"github.com/creydr/ai-coworker/internal/executor/claudecode"
	"github.com/creydr/ai-coworker/internal/executor/llmexec"
	"github.com/creydr/ai-coworker/internal/llm"
	"github.com/creydr/ai-coworker/internal/llm/claude"
	"github.com/creydr/ai-coworker/internal/llm/vertex"
	"github.com/creydr/ai-coworker/internal/sandbox/docker"
	"github.com/creydr/ai-coworker/internal/store"
)

func main() {
	// 1. Load config from config.yaml or first CLI argument.
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
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
		var err error
		llmProvider, err = vertex.New(ctx, cfg.LLM.Vertex.ProjectID, cfg.LLM.Vertex.Region, cfg.LLM.Model)
		if err != nil {
			slog.Error("failed to create vertex LLM provider", "error", err)
			os.Exit(1)
		}
	default:
		llmProvider = claude.New(cfg.LLM.APIKey, cfg.LLM.Model)
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
	var githubAdapter *github.Adapter
	if cfg.GitHub.Enabled {
		var err error
		githubAdapter, err = github.New(cfg.GitHub.AppID, []byte(cfg.GitHub.PrivateKey), cfg.GitHub.WebhookSecret, cfg.GitHub.BotUsername)
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
		slog.Info("github adapter enabled")
	}

	// 8. Create Docker sandbox runtime.
	sandboxRuntime, err := docker.New()
	if err != nil {
		slog.Error("failed to create docker sandbox runtime", "error", err)
		os.Exit(1)
	}

	// 9. Create Claude Code executor with sandbox runtime.
	var githubTokenFunc func(ctx context.Context, repo string) (string, error)
	if githubAdapter != nil {
		githubTokenFunc = githubAdapter.CreateInstallationTokenForRepo
	}
	codeExec := claudecode.New(claudecode.Config{
		Runtime:         sandboxRuntime,
		Image:           cfg.Sandbox.Image,
		EnvVars:         map[string]string{"ANTHROPIC_API_KEY": cfg.LLM.APIKey},
		TimeoutSeconds:  cfg.Sandbox.TimeoutSeconds,
		CPULimit:        cfg.Sandbox.CPULimit,
		MemoryLimit:     cfg.Sandbox.MemoryLimit,
		GitHubTokenFunc: githubTokenFunc,
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
}

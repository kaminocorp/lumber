package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaminocorp/lumber/internal/config"
	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/engine"
	"github.com/kaminocorp/lumber/internal/engine/classifier"
	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/engine/dedup"
	"github.com/kaminocorp/lumber/internal/engine/embedder"
	"github.com/kaminocorp/lumber/internal/engine/taxonomy"
	"github.com/kaminocorp/lumber/internal/logging"
	"github.com/kaminocorp/lumber/internal/output"
	"github.com/kaminocorp/lumber/internal/output/async"
	"github.com/kaminocorp/lumber/internal/output/file"
	"github.com/kaminocorp/lumber/internal/output/multi"
	"github.com/kaminocorp/lumber/internal/output/stdout"
	"github.com/kaminocorp/lumber/internal/output/webhook"
	"github.com/kaminocorp/lumber/internal/pipeline"

	// Register connector implementations.
	_ "github.com/kaminocorp/lumber/internal/connector/flyio"
	_ "github.com/kaminocorp/lumber/internal/connector/supabase"
	_ "github.com/kaminocorp/lumber/internal/connector/vercel"
)

func main() {
	cfg := config.LoadWithFlags()

	if cfg.ShowVersion {
		fmt.Printf("lumber %s\n", config.Version)
		os.Exit(0)
	}

	logging.Init(cfg.Output.Format == "stdout", logging.ParseLevel(cfg.LogLevel))

	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Initialize embedder.
	emb, err := embedder.New(cfg.Engine.ModelPath, cfg.Engine.VocabPath, cfg.Engine.ProjectionPath)
	if err != nil {
		slog.Error("failed to create embedder", "error", err)
		os.Exit(1)
	}
	defer emb.Close()
	slog.Info("embedder loaded", "model", cfg.Engine.ModelPath, "dim", emb.EmbedDim())

	// Initialize taxonomy with default labels.
	t0 := time.Now()
	tax, err := taxonomy.New(taxonomy.DefaultRoots(), emb)
	if err != nil {
		slog.Error("failed to create taxonomy", "error", err)
		os.Exit(1)
	}
	slog.Info("taxonomy pre-embedded", "labels", len(tax.Labels()), "duration", time.Since(t0).Round(time.Millisecond))

	// Initialize classifier and compactor.
	cls := classifier.New(cfg.Engine.ConfidenceThreshold)
	cmp := compactor.New(parseVerbosity(cfg.Engine.Verbosity))

	// Initialize engine.
	eng := engine.New(emb, tax, cls, cmp)

	// Initialize output(s).
	verbosity := parseVerbosity(cfg.Engine.Verbosity)
	var outputs []output.Output
	outputs = append(outputs, stdout.New(verbosity, cfg.Output.Pretty))

	if cfg.Output.FilePath != "" {
		var fileOpts []file.Option
		if cfg.Output.FileMaxSize > 0 {
			fileOpts = append(fileOpts, file.WithMaxSize(cfg.Output.FileMaxSize))
		}
		f, err := file.New(cfg.Output.FilePath, verbosity, fileOpts...)
		if err != nil {
			slog.Error("failed to create file output", "error", err)
			os.Exit(1)
		}
		outputs = append(outputs, async.New(f))
		slog.Info("file output enabled", "path", cfg.Output.FilePath)
	}

	if cfg.Output.WebhookURL != "" {
		var whOpts []webhook.Option
		if cfg.Output.WebhookHeaders != nil {
			whOpts = append(whOpts, webhook.WithHeaders(cfg.Output.WebhookHeaders))
		}
		wh := webhook.New(cfg.Output.WebhookURL, whOpts...)
		outputs = append(outputs, async.New(wh, async.WithDropOnFull()))
		slog.Info("webhook output enabled", "url", cfg.Output.WebhookURL)
	}

	out := multi.New(outputs...)

	// Resolve connector.
	ctor, err := connector.Get(cfg.Connector.Provider)
	if err != nil {
		slog.Error("failed to get connector", "error", err)
		os.Exit(1)
	}
	conn := ctor()

	// Build pipeline with optional dedup.
	var pipeOpts []pipeline.Option
	if cfg.Engine.DedupWindow > 0 {
		d := dedup.New(dedup.Config{Window: cfg.Engine.DedupWindow})
		pipeOpts = append(pipeOpts, pipeline.WithDedup(d, cfg.Engine.DedupWindow))
		slog.Info("dedup enabled", "window", cfg.Engine.DedupWindow)
	}
	if cfg.Engine.MaxBufferSize > 0 {
		pipeOpts = append(pipeOpts, pipeline.WithMaxBufferSize(cfg.Engine.MaxBufferSize))
	}
	p := pipeline.New(conn, eng, out, pipeOpts...)
	defer p.Close()

	// Set up graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 2) // buffer 2 to catch second signal
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig, "timeout", cfg.ShutdownTimeout)
		cancel()

		// Shutdown timer — force exit if drain exceeds timeout.
		timer := time.NewTimer(cfg.ShutdownTimeout)
		defer timer.Stop()

		select {
		case sig := <-sigCh:
			slog.Warn("second signal, forcing exit", "signal", sig)
			os.Exit(1)
		case <-timer.C:
			slog.Error("shutdown timeout exceeded, forcing exit", "timeout", cfg.ShutdownTimeout)
			os.Exit(1)
		}
	}()

	// Start pipeline.
	connCfg := connector.ConnectorConfig{
		Provider: cfg.Connector.Provider,
		APIKey:   cfg.Connector.APIKey,
		Endpoint: cfg.Connector.Endpoint,
		Extra:    cfg.Connector.Extra,
	}

	switch cfg.Mode {
	case "query":
		slog.Info("starting query", "connector", cfg.Connector.Provider,
			"from", cfg.QueryFrom, "to", cfg.QueryTo, "limit", cfg.QueryLimit)
		params := connector.QueryParams{
			Start: cfg.QueryFrom,
			End:   cfg.QueryTo,
			Limit: cfg.QueryLimit,
		}
		if err := p.Query(ctx, connCfg, params); err != nil {
			slog.Error("query failed", "error", err)
			os.Exit(1)
		}
	default: // "stream"
		slog.Info("starting stream", "connector", cfg.Connector.Provider)
		if err := p.Stream(ctx, connCfg); err != nil && err != context.Canceled {
			slog.Error("pipeline error", "error", err)
			os.Exit(1)
		}
	}
}

func parseVerbosity(s string) compactor.Verbosity {
	switch s {
	case "minimal":
		return compactor.Minimal
	case "full":
		return compactor.Full
	default:
		return compactor.Standard
	}
}

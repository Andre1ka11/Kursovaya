package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"study-agent/internal/app"
	"study-agent/internal/config"
	"study-agent/internal/service"
	"study-agent/internal/telegram"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "configs/agent.yaml", "path to the YAML configuration")
	checkConfig := flag.Bool("check-config", false, "validate configuration and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments:", flag.Args())
		return 1
	}

	cfg, err := config.Load(*configPath, ".env")
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return 1
	}

	options := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, options)
	} else {
		handler = slog.NewTextHandler(os.Stderr, options)
	}
	logger := slog.New(handler).With("app", "study-agent")

	if *checkConfig {
		logger.Info("configuration valid", "config", *configPath, "timezone", cfg.Timezone.String(), "telegram_enabled", cfg.Telegram.Enabled)
		return 0
	}

	// This context requests shutdown. It is not the timeout for draining workers.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var workers []app.Worker
	if cfg.Telegram.Enabled {
		client, err := telegram.New(cfg.Telegram, service.Commands{}, logger)
		if err != nil {
			logger.Error("cannot initialize Telegram bot", "error", err)
			return 1
		}
		workers = append(workers, client)
	}
	if err := app.New(cfg, logger, workers...).Run(ctx); err != nil {
		logger.Error("agent failed", "error", err)
		return 1
	}
	return 0
}

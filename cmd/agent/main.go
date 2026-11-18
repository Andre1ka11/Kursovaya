package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"study-agent/internal/app"
	"study-agent/internal/config"
	"study-agent/internal/service"
	"study-agent/internal/storage/postgres"
	"study-agent/internal/telegram"
)

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	configPath := flag.String("config", "configs/agent.yaml", "path to the YAML configuration")
	checkConfig := flag.Bool("check-config", false, "validate configuration and exit")
	checkDB := flag.Bool("check-db", false, "connect to PostgreSQL and exit without migrations")
	migrate := flag.Bool("migrate", false, "apply database migrations and exit")
	flag.Parse()
	if (*checkConfig && (*checkDB || *migrate)) || (*checkDB && *migrate) {
		fmt.Fprintln(os.Stderr, "choose only one of -check-config, -check-db and -migrate")
		return 1
	}
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
		logger.Info("configuration valid", "config", *configPath, "timezone", cfg.Timezone.String(), "telegram_enabled", cfg.Telegram.Enabled, "database_enabled", cfg.Database.Enabled)
		return 0
	}

	// This context requests shutdown. It is not the timeout for draining workers.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if (*checkDB || *migrate) && !cfg.Database.Enabled {
		logger.Error("database is disabled; set AGENT_DATABASE_ENABLED=true")
		return 1
	}
	if cfg.Database.Enabled {
		db, err := postgres.Open(ctx, cfg.Database)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			logger.Error("cannot connect to database", "error", err)
			return 1
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if err := db.Close(closeCtx); err != nil {
				logger.Error("database close timed out")
				exitCode = 1
			} else {
				logger.Info("database closed")
			}
		}()
		logger.Info("database connected")
		if *checkDB {
			return 0
		}
		migrationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = db.Migrate(migrationCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			logger.Error("database migration failed", "error", err)
			return 1
		}
		logger.Info("database migrations applied")
		if *migrate {
			return 0
		}
	}

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

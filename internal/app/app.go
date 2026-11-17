// Package app owns the application's background work and shutdown sequence.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"study-agent/internal/config"
)

type App struct {
	config config.Config
	logger *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger) *App {
	return &App{config: cfg, logger: logger}
}

// Run blocks until shutdown is requested, then waits for background workers.
func (a *App) Run(shutdownRequested context.Context) error {
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		a.heartbeat(workerCtx)
	}()

	a.logger.Info("agent started", "timezone", a.config.Timezone.String())
	<-shutdownRequested.Done()
	a.logger.Info("shutdown requested")

	// Stop background work before closing shared resources such as a database pool.
	stopWorkers()
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()

	// An already-cancelled signal context cannot provide time to finish shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeout)
	defer cancel()
	if err := waitForWorkers(shutdownCtx, done); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	a.logger.Info("agent stopped")
	return nil
}

func (a *App) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(a.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.logger.Debug("agent heartbeat")
		}
	}
}

func waitForWorkers(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"study-agent/internal/config"
)

func TestRunStopsBackgroundWorkOnCancellation(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	agent := New(config.Config{
		Timezone:          time.UTC,
		ShutdownTimeout:   time.Second,
		HeartbeatInterval: time.Hour,
	}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- agent.Run(ctx) }()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("application did not stop after cancellation")
	}
	// Run has joined all workers; no goroutine is writing to output at this point.
	logs := output.String()
	last := -1
	for _, message := range []string{"agent started", "shutdown requested", "agent stopped"} {
		index := strings.Index(logs, message)
		if index <= last {
			t.Fatalf("missing or out-of-order lifecycle message %q: %s", message, logs)
		}
		last = index
	}
}

func TestWaitForWorkersCompletes(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if err := waitForWorkers(context.Background(), done); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForWorkersHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := waitForWorkers(ctx, make(chan struct{})); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

type workerFunc func(context.Context) error

func (w workerFunc) Run(ctx context.Context) error { return w(ctx) }

func testConfig() config.Config {
	return config.Config{Timezone: time.UTC, ShutdownTimeout: time.Second, HeartbeatInterval: time.Hour}
}

func TestWorkerFailureStopsOtherWorkers(t *testing.T) {
	want := errors.New("worker failed")
	stopped := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	agent := New(testConfig(), logger,
		workerFunc(func(context.Context) error { return want }),
		workerFunc(func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil }),
	)
	if err := agent.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected worker error, got %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Run returned before joining the other worker")
	}
}

func TestUnexpectedWorkerExitFailsApplication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	err := New(testConfig(), logger, workerFunc(func(context.Context) error { return nil })).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpectedly") {
		t.Fatalf("expected an unexpected worker exit error, got %v", err)
	}
}

func TestShutdownTimeoutBoundsUnresponsiveWorker(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	cfg := testConfig()
	cfg.ShutdownTimeout = 20 * time.Millisecond
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	agent := New(cfg, logger, workerFunc(func(context.Context) error { close(started); <-release; return nil }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- agent.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected shutdown deadline, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout was not respected")
	}
}

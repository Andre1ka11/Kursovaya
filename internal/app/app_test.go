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

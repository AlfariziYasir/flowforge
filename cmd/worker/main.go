package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flowforge/internal/platform/config"
	"flowforge/internal/platform/logger"
)

func main() {
	cfg := config.Load()
	log := logger.Setup(cfg.Environment, cfg.LogLevel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Info("Starting FlowForge Worker service", slog.String("env", cfg.Environment), slog.String("redisUrl", cfg.RedisURL))

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		log.Info("Worker received shutdown signal", slog.String("signal", sig.String()))
		cancel()
	case <-ctx.Done():
	}

	log.Info("Waiting for worker goroutines to drain...")
	// TODO(Phase 5): Replace fixed sleep with sync.WaitGroup / Asynq worker server Shutdown() coordination.
	time.Sleep(100 * time.Millisecond)
	log.Info("Worker service stopped cleanly")
}

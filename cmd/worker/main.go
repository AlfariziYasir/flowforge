package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	redisclient "github.com/redis/go-redis/v9"

	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/config"
	"flowforge/internal/platform/eventbus"
	"flowforge/internal/platform/logger"
	"flowforge/internal/platform/metrics"
	"flowforge/internal/platform/postgres"
	"flowforge/internal/platform/queue"
	"flowforge/internal/platform/redis"
	"flowforge/internal/platform/safehttp"
	"flowforge/internal/workflow"
)

func isProductionLike(env string) bool {
	return env == "production" || env == "staging"
}

func main() {
	cfg := config.Load()
	log := logger.Setup(cfg.Environment, cfg.LogLevel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Info("Starting FlowForge Worker service",
		slog.String("env", cfg.Environment),
		slog.String("redisUrl", logger.RedactURL(cfg.RedisURL)),
		slog.Int("concurrency", cfg.WorkerConcurrency))

	var dbPool *pgxpool.Pool
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("worker requires postgres", slog.String("dbUrl", logger.RedactURL(cfg.DatabaseURL)), slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	var rClient *redisclient.Client
	rConn, err := redis.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Error("worker requires redis", slog.String("redisUrl", logger.RedactURL(cfg.RedisURL)), slog.Any("error", err))
		os.Exit(1)
	}
	rClient = rConn
	defer rClient.Close()

	// Wiring: repositories, SSRF boundary, executors, coordinator, queue.
	verRepo := workflow.NewVersionRepository(dbPool)
	execRepo := execution.NewExecutionRepository(dbPool)
	uow := postgres.NewUnitOfWork(dbPool)

	// B-1: the denylist is the security boundary; ALLOWED_HTTP_HOSTS is a dev-only
	// override, ignored entirely in production-like environments.
	var allowed []string
	if !isProductionLike(cfg.Environment) && strings.TrimSpace(cfg.AllowedHTTP) != "" {
		for _, h := range strings.Split(cfg.AllowedHTTP, ",") {
			if h = strings.TrimSpace(h); h != "" {
				allowed = append(allowed, h)
			}
		}
	}
	ssrf := safehttp.NewSSRFValidator(allowed, isProductionLike(cfg.Environment))
	httpClient := ssrf.Client(30 * time.Second)

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	queueClient := queue.NewClient(rClient)
	defer queueClient.Close()

	// Phase 7 event-ingress port: the worker handles inbound events directly via
	// an EventService (no run-lifecycle or AI deps needed here).
	eventService := execution.NewEventService(execRepo, execRepo, queueClient, uow, log)

	// Phase 7 egress Router: EVENT_PUBLISH selects a transport per node config.
	// "" defaults to the internal queue (Phase 5 behavior unchanged).
	secretGetter := func(ctx context.Context, tenantID uuid.UUID) (string, error) {
		return execRepo.GetWebhookSecret(ctx, tenantID)
	}
	router := eventbus.Router{
		Internal: queueClient,
		GRPC:     eventbus.NewGRPCPublisher(secretGetter),
	}

	// Phase 7 NATS/JetStream ingress (and NATS egress). NATS is optional: the
	// worker runs without it if unavailable.
	var natsWG sync.WaitGroup
	var natsCancel context.CancelFunc
	nc, natsErr := nats.Connect(cfg.NATSURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if natsErr != nil {
		log.Warn("nats unavailable, NATS ingress disabled", slog.String("natsUrl", cfg.NATSURL), slog.Any("error", natsErr))
	} else {
		defer nc.Close()
		js, err := nc.JetStream()
		if err != nil {
			log.Error("nats jetstream unavailable", slog.Any("error", err))
			os.Exit(1)
		}
		if err := eventbus.EnsureStream(js, cfg.NATSStream, "flowforge.events.>"); err != nil {
			log.Error("nats stream provisioning failed", slog.Any("error", err))
			os.Exit(1)
		}
		router.NATS = eventbus.NewNATSPublisher(js, secretGetter)

		natsCtx, cancel := context.WithCancel(ctx)
		natsCancel = cancel
		natsWG.Add(1)
		go func() {
			defer natsWG.Done()
			if err := eventbus.Subscribe(natsCtx, nc, "flowforge.events.>", "flowforge-worker", secretGetter, eventService, log); err != nil {
				log.Error("nats subscriber exited", slog.Any("error", err))
			}
		}()
	}

	execs := executor.NewRegistry(httpClient, cfg.StepMaxBodyBytes, router)

	coord := execution.NewCoordinator(
		execRepo, execRepo, execRepo, verRepo, execs,
		execution.CoordinatorConfig{
			Concurrency:  cfg.WorkerConcurrency,
			Lease:        cfg.RunLeaseDuration,
			Retry:        engine.DefaultRetryPolicy(),
			Timeout:      engine.DefaultTimeoutPolicy(),
			MaxBodyBytes: cfg.StepMaxBodyBytes,
			WorkerID:     "worker-" + hostname(),
			Logger:       log,
			HTTPClient:   httpClient,
			Metrics:      m,
			Publisher:    queueClient,
		},
	)

	// Reaper: reclaim expired leases and re-enqueue them so a crashed worker's
	// run is resumed by a fresh one; also reports queue depth.
	var reaperWG sync.WaitGroup
	reaperCtx, stopReaper := context.WithCancel(ctx)
	reaperWG.Add(1)
	go reaperLoop(reaperCtx, &reaperWG, log, execRepo, execRepo, queueClient, m)

	// Consume run wake-ups. Every goroutine asynq spawns is owned by its server
	// and drained by Shutdown.
	server, handler := queue.NewServer(rClient, cfg.WorkerConcurrency, func(hctx context.Context, tenantID, runID uuid.UUID) error {
		return coord.HandleRun(hctx, tenantID, runID)
	})

	if err := server.Start(handler); err != nil {
		log.Error("asynq server failed to start", slog.Any("error", err))
		os.Exit(1)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Worker received shutdown signal; draining in-flight tasks...")
	stopReaper()
	reaperWG.Wait()
	if natsCancel != nil {
		natsCancel()
	}
	natsWG.Wait()
	server.Shutdown()
	log.Info("Worker service stopped cleanly")
}

// reaperLoop reclaims runs whose lease expired (crash recovery) and enqueues
// them for a fresh worker, then publishes queue depth.
func reaperLoop(ctx context.Context, wg *sync.WaitGroup, log *slog.Logger,
	runs execution.RunRepository, tokens execution.WaitTokenRepository, qc *queue.Client, m *metrics.Metrics) {
	defer wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reclaimed, err := runs.ReclaimExpiredLeases(ctx)
			if err != nil {
				log.Error("reaper failed", slog.Any("error", err))
				continue
			}
			for _, r := range reclaimed {
				if err := qc.EnqueueRun(r.TenantID, r.RunID); err != nil {
					log.Error("reaper re-enqueue failed", slog.String("runID", r.RunID.String()), slog.Any("error", err))
					continue
				}
				log.Info("reclaimed expired lease and re-enqueued run", slog.String("runID", r.RunID.String()))
			}

			// Phase 7 sweeper: expired wait tokens fail their step and wake the run
			// so it walks the error path instead of hanging on a token that never
			// resolves.
			if _, err := execution.SweepExpiredWaitTokens(ctx, tokens, runs, qc, log); err != nil {
				log.Error("wait-token sweep failed", slog.Any("error", err))
			}

			// Lost-enqueue recovery: pending runs that were never claimed (their
			// enqueue was lost) get re-enqueued. Re-running this every tick is
			// harmless — duplicate delivery is a documented no-op.
			if stale, err := runs.ReclaimStalePendingRuns(ctx, 30*time.Second); err == nil {
				for _, r := range stale {
					if err := qc.EnqueueRun(r.TenantID, r.RunID); err != nil {
						log.Error("stale re-enqueue failed", slog.String("runID", r.RunID.String()), slog.Any("error", err))
						continue
					}
					log.Info("re-enqueued stale pending run", slog.String("runID", r.RunID.String()))
				}
			} else {
				log.Warn("stale-pending reclaim failed", slog.Any("error", err))
			}

			if depth, err := qc.QueueDepth(); err == nil {
				m.QueueDepth.WithLabelValues(queue.RunQueue).Set(float64(depth))
			}
		}
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flowforge/internal/auth"
	"flowforge/internal/execution"
	"flowforge/internal/platform/ai"
	"flowforge/internal/platform/audit"
	"flowforge/internal/platform/config"
	"flowforge/internal/platform/eventbus"
	"flowforge/internal/platform/eventbus/eventspb"
	"flowforge/internal/platform/eventstream"
	"flowforge/internal/platform/httpmw"
	"flowforge/internal/platform/logger"
	"flowforge/internal/platform/metrics"
	"flowforge/internal/platform/postgres"
	"flowforge/internal/platform/queue"
	"flowforge/internal/platform/ratelimit"
	"flowforge/internal/platform/redis"
	"flowforge/internal/tenant"
	"flowforge/internal/workflow"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	redisclient "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

// Pinger abstracts active connection checks for dependencies like PostgreSQL and Redis.
type Pinger interface {
	Ping(ctx context.Context) error
}

// RedisPingerAdapter adapts *redis.Client to the Pinger interface.
type RedisPingerAdapter struct {
	client *redisclient.Client
}

func (r *RedisPingerAdapter) Ping(ctx context.Context) error {
	if r.client == nil {
		return errors.New("redis client is nil")
	}
	return r.client.Ping(ctx).Err()
}

// HealthChecker handles active health checks for system dependencies.
type HealthChecker struct {
	DB    Pinger
	Redis Pinger
}

func (h *HealthChecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	dbStatus := "up"
	redisStatus := "up"
	isHealthy := true

	if h.DB != nil {
		if err := h.DB.Ping(ctx); err != nil {
			dbStatus = "down"
			isHealthy = false
		}
	} else {
		dbStatus = "down"
		isHealthy = false
	}

	if h.Redis != nil {
		if err := h.Redis.Ping(ctx); err != nil {
			redisStatus = "down"
			isHealthy = false
		}
	} else {
		redisStatus = "down"
		isHealthy = false
	}

	w.Header().Set("Content-Type", "application/json")
	if isHealthy {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]string{
				"status":   "ok",
				"service":  "api",
				"postgres": dbStatus,
				"redis":    redisStatus,
			},
			"meta":  nil,
			"error": nil,
		})
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"data": map[string]string{
			"status":   "unhealthy",
			"service":  "api",
			"postgres": dbStatus,
			"redis":    redisStatus,
		},
		"meta": nil,
		"error": map[string]interface{}{
			"code":    "SERVICE_UNAVAILABLE",
			"message": "Health check failed for dependencies",
			"details": nil,
		},
	})
}

// NewRouter registers HTTP routes for the API service.
func NewRouter(
	hc *HealthChecker,
	authHandler *auth.AuthHandler,
	userHandler *auth.UserHandler,
	workflowHandler *workflow.WorkflowHandler,
	executionHandler *execution.ExecutionHandler,
	authMiddleware *auth.AuthMiddleware,
) *http.ServeMux {
	return NewRouterWithLimiter(hc, authHandler, userHandler, workflowHandler, executionHandler, authMiddleware, nil, nil, false)
}

// NewRouterWithLimiter registers HTTP routes with optional Prometheus metrics and Redis rate limiting.
func NewRouterWithLimiter(
	hc *HealthChecker,
	authHandler *auth.AuthHandler,
	userHandler *auth.UserHandler,
	workflowHandler *workflow.WorkflowHandler,
	executionHandler *execution.ExecutionHandler,
	authMiddleware *auth.AuthMiddleware,
	reg prometheus.Gatherer,
	limiter *ratelimit.Limiter,
	trustProxyHeaders bool,
) *http.ServeMux {
	mux := http.NewServeMux()
	if hc != nil {
		mux.Handle("/health", hc)
		mux.Handle("/api/v1/health", hc)
	}

	if reg != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	}

	loginLimit := func(h http.Handler) http.Handler {
		if limiter == nil {
			return h
		}
		return ratelimit.Middleware(limiter, 10, time.Minute, ratelimit.IPKeyFunc("login", trustProxyHeaders))(h)
	}

	triggerRunLimit := func(h http.Handler) http.Handler {
		if limiter == nil {
			return h
		}
		return ratelimit.Middleware(limiter, 30, time.Minute, ratelimit.TenantKeyFunc("trigger_run"))(h)
	}

	generalLimit := func(h http.Handler) http.Handler {
		if limiter == nil {
			return h
		}
		return ratelimit.Middleware(limiter, 120, time.Minute, ratelimit.TenantKeyFunc("general"))(h)
	}

	if authHandler != nil {
		mux.Handle("POST /api/v1/auth/login", loginLimit(http.HandlerFunc(authHandler.Login)))
		mux.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)
		mux.HandleFunc("POST /api/v1/auth/logout", authHandler.Logout)

		if authMiddleware != nil {
			mux.Handle("POST /api/v1/auth/logout-all", authMiddleware.Authenticate(generalLimit(http.HandlerFunc(authHandler.LogoutAll))))
			mux.Handle("GET /api/v1/auth/sessions", authMiddleware.Authenticate(generalLimit(http.HandlerFunc(authHandler.ListSessions))))
			mux.Handle("GET /api/v1/users/me", authMiddleware.Authenticate(generalLimit(http.HandlerFunc(authHandler.GetMe))))
		}
	}

	if userHandler != nil && authMiddleware != nil {
		mux.Handle("POST /api/v1/users", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin")(http.HandlerFunc(userHandler.CreateUser)))))
		mux.Handle("GET /api/v1/users", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(userHandler.ListUsers)))))
		mux.Handle("GET /api/v1/users/{userId}", authMiddleware.Authenticate(generalLimit(http.HandlerFunc(userHandler.GetUser))))
		mux.Handle("PATCH /api/v1/users/{userId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin")(http.HandlerFunc(userHandler.UpdateUser)))))
		mux.Handle("DELETE /api/v1/users/{userId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin")(http.HandlerFunc(userHandler.DeleteUser)))))
	}

	if workflowHandler != nil && authMiddleware != nil {
		mux.Handle("POST /api/v1/workflows", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(workflowHandler.Create)))))
		mux.Handle("GET /api/v1/workflows", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(workflowHandler.List)))))
		mux.Handle("GET /api/v1/workflows/{workflowId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(workflowHandler.Get)))))
		mux.Handle("PATCH /api/v1/workflows/{workflowId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(workflowHandler.Update)))))
		mux.Handle("DELETE /api/v1/workflows/{workflowId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(workflowHandler.Archive)))))
		mux.Handle("PUT /api/v1/workflows/{workflowId}/draft", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(workflowHandler.SaveDraft)))))
		mux.Handle("POST /api/v1/workflows/{workflowId}/versions/publish", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(workflowHandler.Publish)))))
		mux.Handle("POST /api/v1/workflows/{workflowId}/versions/{versionId}/rollback", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(workflowHandler.Rollback)))))
		mux.Handle("GET /api/v1/workflows/{workflowId}/versions", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(workflowHandler.ListVersions)))))
		mux.Handle("GET /api/v1/workflows/{workflowId}/versions/{versionId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(workflowHandler.GetVersion)))))
	}

	if executionHandler != nil && authMiddleware != nil {
		mux.Handle("POST /api/v1/workflows/{workflowId}/runs", authMiddleware.Authenticate(triggerRunLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(executionHandler.TriggerRun)))))
		mux.Handle("GET /api/v1/workflows/{workflowId}/runs", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(executionHandler.ListRuns)))))
		mux.Handle("GET /api/v1/workflow-runs/{runId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(executionHandler.GetRun)))))
		mux.Handle("POST /api/v1/workflow-runs/{runId}/cancel", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(executionHandler.CancelRun)))))
		mux.Handle("POST /api/v1/workflow-runs/{runId}/retry", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(executionHandler.RetryRun)))))
		mux.Handle("GET /api/v1/workflow-runs/{runId}/steps", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(executionHandler.ListSteps)))))
		mux.Handle("GET /api/v1/workflow-runs/{runId}/steps/{stepRunId}", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(executionHandler.GetStep)))))
		mux.Handle("GET /api/v1/workflow-runs/{runId}/logs", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(executionHandler.ListLogs)))))
		mux.Handle("POST /api/v1/workflow-runs/{runId}/analysis", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin", "editor", "viewer")(http.HandlerFunc(executionHandler.AnalyzeRun)))))

		// Phase 8 Real-time Monitoring SSE stream (authenticated via header or ?token=)
		mux.Handle("GET /api/v1/events", authMiddleware.AuthenticateWithTokenSource(true)(http.HandlerFunc(executionHandler.StreamEvents)))

		// Event ingress is authenticated by HMAC, not JWT — external systems have
		// no user session. Secret rotation stays behind the admin role.
		mux.HandleFunc("POST /api/v1/tenants/{tenantId}/events", executionHandler.IngestEvent)
		mux.Handle("POST /api/v1/tenants/{tenantId}/webhook-secret/rotate", authMiddleware.Authenticate(generalLimit(auth.RequireRole("admin")(http.HandlerFunc(executionHandler.RotateWebhookSecret)))))
	}

	return mux
}

const devJWTSecret = "flowforge-dev-secret-change-in-prod-12345"

func isProductionLike(env string) bool {
	return env == "production" || env == "staging"
}

func applyJWTSecretDefault(cfg *config.Config) {
	if cfg.JWTSecret == "" && !isProductionLike(cfg.Environment) {
		cfg.JWTSecret = devJWTSecret
	}
}

func validateJWTSecret(cfg *config.Config) error {
	if cfg.JWTSecret == "" {
		if isProductionLike(cfg.Environment) {
			return errors.New("JWT_SECRET environment variable must be set in production/staging")
		}
	}
	if (len(cfg.JWTSecret) < 32 || cfg.JWTSecret == devJWTSecret) && isProductionLike(cfg.Environment) {
		return errors.New("JWT_SECRET must be set and at least 32 characters long in production/staging")
	}
	return nil
}

func main() {
	cfg := config.Load()
	log := logger.Setup(cfg.Environment, cfg.LogLevel)

	applyJWTSecretDefault(cfg)
	if err := validateJWTSecret(cfg); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var dbPool *pgxpool.Pool
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Warn("failed to connect to postgresql during startup", slog.String("dbUrl", logger.RedactURL(cfg.DatabaseURL)), slog.Any("error", err))
	} else {
		dbPool = pool
		defer dbPool.Close()
	}

	var rClient *redisclient.Client
	rConn, err := redis.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		if isProductionLike(cfg.Environment) {
			log.Error("Redis connection is required in production/staging environment", slog.String("redisUrl", logger.RedactURL(cfg.RedisURL)), slog.Any("error", err))
			os.Exit(1)
		}
		log.Warn("failed to connect to redis during startup", slog.String("redisUrl", logger.RedactURL(cfg.RedisURL)), slog.Any("error", err))
	} else {
		rClient = rConn
		defer rClient.Close()
	}

	hc := &HealthChecker{}
	if dbPool != nil {
		hc.DB = dbPool
	}
	if rClient != nil {
		hc.Redis = &RedisPingerAdapter{client: rClient}
	}

	var authHandler *auth.AuthHandler
	var userHandler *auth.UserHandler
	var workflowHandler *workflow.WorkflowHandler
	var executionHandler *execution.ExecutionHandler
	var grpcSrv *grpc.Server
	var authMiddleware *auth.AuthMiddleware

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	if dbPool != nil {
		tenantRepo := tenant.NewTenantRepository(dbPool)
		userRepo := auth.NewUserRepository(dbPool)
		uow := postgres.NewUnitOfWork(dbPool)
		jwtSecret := cfg.JWTSecret

		var blacklist auth.TokenBlacklist
		var sessionStore auth.SessionStore
		if rClient != nil {
			blacklist = auth.NewRedisTokenBlacklist(rClient)
			sessionStore = auth.NewRedisSessionStoreWithTTL(rClient, cfg.JWTRefreshExpiry)
		} else {
			blacklist = auth.NewNoopTokenBlacklist()
			sessionStore = auth.NewNoopSessionStore()
		}

		jwtSvc := auth.NewJWTService(jwtSecret, cfg.JWTAccessExpiry, cfg.JWTRefreshExpiry)
		passSvc := auth.NewPasswordService()
		auditRepo := audit.NewAuditRepository(dbPool)

		tenantUC := tenant.NewTenantUseCase(tenantRepo)
		authUC := auth.NewAuthUseCaseWithAudit(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, cfg.JWTRefreshExpiry, auditRepo, log)
		userUC := auth.NewUserUseCaseWithTxAndAudit(userRepo, passSvc, sessionStore, cfg.JWTRefreshExpiry, uow, auditRepo)

		wfRepo := workflow.NewWorkflowRepository(dbPool)
		verRepo := workflow.NewVersionRepository(dbPool)
		wfUC := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, uow)

		execRepo := execution.NewExecutionRepository(dbPool)
		if rClient != nil {
			queueClient := queue.NewClient(rClient)
			defer queueClient.Close()
			aiProvider := ai.NewDeepSeekProvider(ai.Config{
				APIKey:         cfg.AIProviderAPIKey,
				Model:          cfg.AIModel,
				BaseURL:        cfg.AIBaseURL,
				RequestTimeout: cfg.AIRequestTimeout,
			})
			eventPub := eventstream.NewRedisPublisher(rClient)
			clientMgr := eventstream.NewClientManager(rClient, log)
			defer func() { _ = clientMgr.Close() }()

			// wfUC satisfies WorkflowReader (GetWorkflow); verRepo satisfies
			// execution.GraphLoader structurally (LoadGraph) — zero new instances.
			execUC := execution.NewExecutionUseCase(
				wfUC, execRepo, execRepo, execRepo, verRepo, queueClient, auditRepo, uow, aiProvider, execRepo,
				execution.ExecutionConfig{
					AIMaxRetries:     cfg.AIMaxRetries,
					AIRequestTimeout: cfg.AIRequestTimeout,
					Events:           eventPub,
					Metrics:          m,
					Logger:           log,
				},
			)
			executionHandler = execution.NewExecutionHandlerWithStream(execUC, clientMgr, m)

			// Phase 7 gRPC event ingress: a second listener on GRPC_PORT sharing
			// the same use-case instance. Auth is the shared webhookauth HMAC
			// interceptor, not JWT.
			secretGetter := func(ctx context.Context, tenantID uuid.UUID) (string, error) {
				return execRepo.GetWebhookSecret(ctx, tenantID)
			}
			grpcSrv = grpc.NewServer(grpc.UnaryInterceptor(eventbus.AuthInterceptor(secretGetter)))
			eventspb.RegisterEventListenerServer(grpcSrv, eventbus.NewEventListenerServerWithTriggerer(execUC, execUC))
		}

		authHandler = auth.NewAuthHandlerWithTrustProxy(authUC, cfg.TrustProxyHeaders)
		userHandler = auth.NewUserHandler(userUC)
		workflowHandler = workflow.NewWorkflowHandler(wfUC)
		authMiddleware = auth.NewAuthMiddlewareWithSessionStore(jwtSvc, blacklist, sessionStore)
	}

	var limiter *ratelimit.Limiter
	if rClient != nil {
		limiter = ratelimit.NewLimiter(rClient, log)
	}

	router := NewRouterWithLimiter(hc, authHandler, userHandler, workflowHandler, executionHandler, authMiddleware, reg, limiter, cfg.TrustProxyHeaders)

	var handler http.Handler = router
	if len(cfg.CORSAllowedOrigins) > 0 {
		handler = httpmw.CORS(cfg.CORSAllowedOrigins, cfg.CORSAllowCredentials)(handler)
	}
	handler = httpmw.SecurityHeaders()(handler)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("Starting FlowForge API service", slog.Int("port", cfg.Port), slog.String("env", cfg.Environment))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Phase 7 gRPC event ingress on a second listener, owned by the same signal
	// handler and drained on shutdown.
	if grpcSrv != nil {
		go func() {
			lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
			if err != nil {
				log.Error("failed to start gRPC listener", slog.Any("error", err))
				serverErr <- err
				return
			}
			log.Info("Starting FlowForge gRPC event listener", slog.String("port", cfg.GRPCPort))
			if err := grpcSrv.Serve(lis); err != nil {
				log.Error("gRPC server failed", slog.Any("error", err))
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("API server crashed", slog.Any("error", err))
		return
	case <-quit:
	}

	log.Info("Shutting down API server gracefully...")
	if grpcSrv != nil {
		grpcSrv.GracefulStop()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("Server forced to shutdown", slog.Any("error", err))
	} else {
		log.Info("API server stopped cleanly")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flowforge/internal/auth"
	"flowforge/internal/platform/config"
	"flowforge/internal/platform/logger"
	"flowforge/internal/platform/postgres"
	"flowforge/internal/platform/redis"
	"flowforge/internal/tenant"

	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"
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
	authMiddleware *auth.AuthMiddleware,
) *http.ServeMux {
	mux := http.NewServeMux()
	if hc != nil {
		mux.Handle("/health", hc)
		mux.Handle("/api/v1/health", hc)
	}

	if authHandler != nil {
		mux.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
		mux.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)
		mux.HandleFunc("POST /api/v1/auth/logout", authHandler.Logout)

		if authMiddleware != nil {
			mux.Handle("POST /api/v1/auth/logout-all", authMiddleware.Authenticate(http.HandlerFunc(authHandler.LogoutAll)))
			mux.Handle("GET /api/v1/auth/sessions", authMiddleware.Authenticate(http.HandlerFunc(authHandler.ListSessions)))
			mux.Handle("GET /api/v1/users/me", authMiddleware.Authenticate(http.HandlerFunc(authHandler.GetMe)))
		}
	}

	if userHandler != nil && authMiddleware != nil {
		mux.Handle("POST /api/v1/users", authMiddleware.Authenticate(auth.RequireRole("admin")(http.HandlerFunc(userHandler.CreateUser))))
		mux.Handle("GET /api/v1/users", authMiddleware.Authenticate(auth.RequireRole("admin", "editor")(http.HandlerFunc(userHandler.ListUsers))))
		mux.Handle("GET /api/v1/users/{userId}", authMiddleware.Authenticate(http.HandlerFunc(userHandler.GetUser)))
		mux.Handle("PATCH /api/v1/users/{userId}", authMiddleware.Authenticate(auth.RequireRole("admin")(http.HandlerFunc(userHandler.UpdateUser))))
		mux.Handle("DELETE /api/v1/users/{userId}", authMiddleware.Authenticate(auth.RequireRole("admin")(http.HandlerFunc(userHandler.DeleteUser))))
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
	var authMiddleware *auth.AuthMiddleware

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

		tenantUC := tenant.NewTenantUseCase(tenantRepo)
		authUC := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, cfg.JWTRefreshExpiry)
		userUC := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, cfg.JWTRefreshExpiry, uow)

		authHandler = auth.NewAuthHandlerWithTrustProxy(authUC, cfg.TrustProxyHeaders)
		userHandler = auth.NewUserHandler(userUC)
		authMiddleware = auth.NewAuthMiddlewareWithSessionStore(jwtSvc, blacklist, sessionStore)
	}

	router := NewRouter(hc, authHandler, userHandler, authMiddleware)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("API server crashed", slog.Any("error", err))
		return
	case <-quit:
	}

	log.Info("Shutting down API server gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("Server forced to shutdown", slog.Any("error", err))
	} else {
		log.Info("API server stopped cleanly")
	}
}

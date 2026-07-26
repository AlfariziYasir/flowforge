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
func NewRouter(hc *HealthChecker, authHandler *auth.AuthHandler, authMiddleware *auth.AuthMiddleware) *http.ServeMux {
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
			mux.Handle("GET /api/v1/users/me", authMiddleware.Authenticate(http.HandlerFunc(authHandler.GetMe)))
		}
	}

	return mux
}

func main() {
	cfg := config.Load()
	log := logger.Setup(cfg.Environment, cfg.LogLevel)

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
	var authMiddleware *auth.AuthMiddleware

	if dbPool != nil {
		tenantRepo := tenant.NewTenantRepository(dbPool)
		userRepo := auth.NewUserRepository(dbPool)
		jwtSecret := os.Getenv("JWT_SECRET")
		if jwtSecret == "" {
			if cfg.Environment == "production" || cfg.Environment == "staging" {
				log.Error("JWT_SECRET environment variable must be set in production/staging")
				os.Exit(1)
			}
			jwtSecret = "flowforge-dev-secret-change-in-prod-12345"
		}
		if len(jwtSecret) < 32 && (cfg.Environment == "production" || cfg.Environment == "staging") {
			log.Error("JWT_SECRET must be at least 32 characters long in production/staging")
			os.Exit(1)
		}

		var blacklist auth.TokenBlacklist
		if rClient != nil {
			blacklist = auth.NewRedisTokenBlacklist(rClient)
		} else {
			blacklist = auth.NewNoopTokenBlacklist()
		}

		jwtSvc := auth.NewJWTService(jwtSecret, 15*time.Minute, 7*24*time.Hour)
		passSvc := auth.NewPasswordService()

		authHandler = auth.NewAuthHandlerWithBlacklist(tenantRepo, userRepo, jwtSvc, passSvc, blacklist)
		authMiddleware = auth.NewAuthMiddlewareWithBlacklist(jwtSvc, blacklist)
	}

	router := NewRouter(hc, authHandler, authMiddleware)
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

package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"flowforge/internal/auth"
	"flowforge/internal/platform/httpx"
)

// RedisClient defines the minimal subset of Redis operations required for rate limiting.
type RedisClient interface {
	Incr(ctx context.Context, key string) *redis.IntCmd
	Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd
}

// Limiter enforces a fixed-window request quota per key, backed by Redis.
type Limiter struct {
	rdb    RedisClient
	logger *slog.Logger
}

// NewLimiter creates a new Redis-backed rate limiter.
func NewLimiter(rdb RedisClient, logger *slog.Logger) *Limiter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Limiter{
		rdb:    rdb,
		logger: logger,
	}
}

// Allow checks if the given key has exceeded the limit within window.
// If Redis is nil or returns an error, it fails open (returns true, err).
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if l == nil || l.rdb == nil {
		return true, nil
	}

	fullKey := fmt.Sprintf("ratelimit:%s", key)
	count, err := l.rdb.Incr(ctx, fullKey).Result()
	if err != nil {
		l.logger.Warn("ratelimit: redis incr failed, failing open",
			slog.String("key", key),
			slog.Any("error", err))
		return true, err
	}

	if count == 1 {
		if err := l.rdb.Expire(ctx, fullKey, window).Err(); err != nil {
			l.logger.Warn("ratelimit: redis expire failed",
				slog.String("key", key),
				slog.Any("error", err))
		}
	}

	return count <= int64(limit), nil
}

// Middleware creates an HTTP middleware that limits requests by keyFunc.
func Middleware(l *Limiter, limit int, window time.Duration, keyFunc func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if l == nil {
				next.ServeHTTP(w, r)
				return
			}

			key := ""
			if keyFunc != nil {
				key = keyFunc(r)
			}
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed, _ := l.Allow(r.Context(), key, limit, window)
			if !allowed {
				w.Header().Set("Retry-After", fmt.Sprintf("%.0f", window.Seconds()))
				httpx.Fail(w, http.StatusTooManyRequests, httpx.CodeRateLimitExceeded, "rate limit exceeded")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// TenantKeyFunc extracts a key based on tenant ID and route group.
func TenantKeyFunc(group string) func(*http.Request) string {
	return func(r *http.Request) string {
		if user, ok := auth.AuthUserFromContext(r.Context()); ok {
			return fmt.Sprintf("tenant:%s:%s", user.TenantID.String(), group)
		}
		return ""
	}
}

// IPKeyFunc extracts client IP for unauthenticated routes.
func IPKeyFunc(group string, trustProxyHeaders bool) func(*http.Request) string {
	return func(r *http.Request) string {
		ip := extractIP(r, trustProxyHeaders)
		if ip == "" {
			return ""
		}
		return fmt.Sprintf("ip:%s:%s", ip, group)
	}
}

func extractIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				clientIP := strings.TrimSpace(parts[0])
				if clientIP != "" {
					return clientIP
				}
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

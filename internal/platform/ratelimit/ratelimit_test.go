package ratelimit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
	"flowforge/internal/platform/ratelimit"
)

type fakeRedis struct {
	mu     sync.Mutex
	counts map[string]int64
	err    error
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{counts: make(map[string]int64)}
}

func (f *fakeRedis) Incr(ctx context.Context, key string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(ctx)
	if f.err != nil {
		cmd.SetErr(f.err)
		return cmd
	}
	f.counts[key]++
	cmd.SetVal(f.counts[key])
	return cmd
}

func (f *fakeRedis) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(true)
	return cmd
}

func TestLimiter_Allow(t *testing.T) {
	t.Run("allows requests within limit", func(t *testing.T) {
		rdb := newFakeRedis()
		l := ratelimit.NewLimiter(rdb, nil)

		allowed, err := l.Allow(context.Background(), "test-key", 2, time.Minute)
		require.NoError(t, err)
		assert.True(t, allowed)

		allowed, err = l.Allow(context.Background(), "test-key", 2, time.Minute)
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("denies requests exceeding limit", func(t *testing.T) {
		rdb := newFakeRedis()
		l := ratelimit.NewLimiter(rdb, nil)

		for i := 0; i < 3; i++ {
			allowed, err := l.Allow(context.Background(), "test-key-2", 2, time.Minute)
			require.NoError(t, err)
			if i < 2 {
				assert.True(t, allowed)
			} else {
				assert.False(t, allowed)
			}
		}
	})

	t.Run("fails open on Redis error", func(t *testing.T) {
		rdb := &fakeRedis{err: errors.New("redis connection refused")}
		l := ratelimit.NewLimiter(rdb, nil)

		allowed, err := l.Allow(context.Background(), "test-key-3", 2, time.Minute)
		assert.Error(t, err)
		assert.True(t, allowed, "Rate limiter MUST fail open when Redis errors (boundary checklist)")
	})
}

func TestLimiter_Middleware(t *testing.T) {
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	t.Run("blocks with 429 when tenant rate limit exceeded", func(t *testing.T) {
		rdb := newFakeRedis()
		l := ratelimit.NewLimiter(rdb, nil)
		mw := ratelimit.Middleware(l, 2, time.Minute, ratelimit.TenantKeyFunc("test_group"))
		handler := mw(dummyHandler)

		tenantID := uuid.New()
		ctx := auth.ContextWithAuthUser(context.Background(), auth.AuthUser{TenantID: tenantID, ID: uuid.New(), Role: "viewer"})

		for i := 0; i < 3; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if i < 2 {
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Equal(t, http.StatusTooManyRequests, rec.Code)
				assert.Equal(t, "60", rec.Header().Get("Retry-After"))
				assert.Contains(t, rec.Body.String(), "RATE_LIMIT_EXCEEDED")
			}
		}
	})

	t.Run("limits by client IP on unauthenticated routes", func(t *testing.T) {
		rdb := newFakeRedis()
		l := ratelimit.NewLimiter(rdb, nil)
		mw := ratelimit.Middleware(l, 1, time.Minute, ratelimit.IPKeyFunc("login", true))
		handler := mw(dummyHandler)

		req1 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req1.Header.Set("X-Forwarded-For", "203.0.113.195")
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)
		assert.Equal(t, http.StatusOK, rec1.Code)

		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req2.Header.Set("X-Forwarded-For", "203.0.113.195")
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)
		assert.Equal(t, http.StatusTooManyRequests, rec2.Code)
	})
}

func TestLimiter_MultiInstanceIntegration(t *testing.T) {
	if os.Getenv("FLOWFORGE_INTEGRATION") != "1" {
		t.Skip("Skipping Redis integration test; FLOWFORGE_INTEGRATION != 1")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	client := redis.NewClient(opts)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Cannot connect to Redis at %s: %v", redisURL, err)
	}

	key := "shared-quota-" + uuid.NewString()
	// Two separate limiter instances sharing one Redis database
	limiter1 := ratelimit.NewLimiter(client, nil)
	limiter2 := ratelimit.NewLimiter(client, nil)

	// Quota is 2 total across both instances
	ok1, err := limiter1.Allow(ctx, key, 2, 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok1)

	ok2, err := limiter2.Allow(ctx, key, 2, 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok2)

	// 3rd attempt across either instance must be denied
	ok3, err := limiter1.Allow(ctx, key, 2, 5*time.Second)
	require.NoError(t, err)
	assert.False(t, ok3, "Redis-backed rate limiter must enforce shared quota across instances")
}

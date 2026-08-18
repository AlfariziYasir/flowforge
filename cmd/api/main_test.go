package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"flowforge/internal/auth"
	authmocks "flowforge/internal/auth/mocks"
	"flowforge/internal/domain"
	"flowforge/internal/platform/config"
	"flowforge/internal/platform/ratelimit"
	"flowforge/internal/workflow"
	workflowmocks "flowforge/internal/workflow/mocks"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestValidateJWTSecret(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		env         string
		expectError bool
	}{
		{
			name:        "unset secret in development defaults to dev secret",
			secret:      "",
			env:         "development",
			expectError: false,
		},
		{
			name:        "unset secret in production is fatal",
			secret:      "",
			env:         "production",
			expectError: true,
		},
		{
			name:        "dev secret in production is fatal",
			secret:      devJWTSecret,
			env:         "production",
			expectError: true,
		},
		{
			name:        "short secret in staging is fatal",
			secret:      "too-short-secret-12345",
			env:         "staging",
			expectError: true,
		},
		{
			name:        "valid 32+ char secret in production is valid",
			secret:      "production-secret-must-be-at-least-32-characters-long!",
			env:         "production",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				JWTSecret:   tt.secret,
				Environment: tt.env,
			}
			applyJWTSecretDefault(cfg)
			err := validateJWTSecret(cfg)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewRouter_UserListRequiresElevatedRole(t *testing.T) {
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	jwtSvc := auth.NewJWTService("router-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
	middleware := auth.NewAuthMiddleware(jwtSvc)

	t.Run("allows admin role to access user list", func(t *testing.T) {
		user := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "admin@flowforge.local",
			Role:     "admin",
		}
		ctx := auth.ContextWithAuthUser(httptest.NewRequest(http.MethodGet, "/api/v1/users", nil).Context(), user)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		auth.RequireRole("admin", "editor")(dummyHandler).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("returns 403 Forbidden for viewer role accessing user list", func(t *testing.T) {
		user := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "viewer@flowforge.local",
			Role:     "viewer",
		}
		ctx := auth.ContextWithAuthUser(httptest.NewRequest(http.MethodGet, "/api/v1/users", nil).Context(), user)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		auth.RequireRole("admin", "editor")(dummyHandler).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("returns 401 Unauthorized for unauthenticated access to user list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		rec := httptest.NewRecorder()

		middleware.Authenticate(dummyHandler).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestNewRouter_WorkflowRoutesWiring(t *testing.T) {
	router := NewRouter(nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNewRouter_PublishVsRollbackRouteDisambiguation(t *testing.T) {
	jwtSvc := auth.NewJWTService("router-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
	middleware := auth.NewAuthMiddleware(jwtSvc)

	wfID := uuid.New()
	verID := uuid.New()
	tenantID := uuid.New()
	userID := uuid.New()

	mockUC := workflowmocks.NewMockWorkflowUseCase(t)
	mockUC.EXPECT().PublishVersion(mock.Anything, mock.Anything).Return(&workflow.PublishResult{WorkflowID: wfID, VersionID: verID, VersionNumber: 1, Status: "published"}, nil).Once()
	mockUC.EXPECT().RollbackVersion(mock.Anything, mock.Anything).Return(&workflow.RollbackResult{WorkflowID: wfID, VersionID: verID, VersionNumber: 2, Status: "draft"}, nil).Once()

	wfHandler := workflow.NewWorkflowHandler(mockUC)
	router := NewRouter(nil, nil, nil, wfHandler, nil, middleware)

	pair, err := jwtSvc.GenerateTokenPair(userID, tenantID, uuid.Nil, "admin@flowforge.local", "admin")
	require.NoError(t, err)

	// Test publish route
	pubBody := []byte(`{"rowVersion":1}`)
	reqPub := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+wfID.String()+"/versions/publish", bytes.NewReader(pubBody))
	reqPub.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	recPub := httptest.NewRecorder()
	router.ServeHTTP(recPub, reqPub)

	assert.Equal(t, http.StatusOK, recPub.Code)

	// Test rollback route
	rollBody := []byte(`{"rowVersion":1}`)
	reqRoll := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+wfID.String()+"/versions/"+verID.String()+"/rollback", bytes.NewReader(rollBody))
	reqRoll.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	recRoll := httptest.NewRecorder()
	router.ServeHTTP(recRoll, reqRoll)

	assert.Equal(t, http.StatusOK, recRoll.Code)
}

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping(ctx context.Context) error {
	return f.err
}

func TestHealthChecker(t *testing.T) {
	tests := []struct {
		name           string
		db             Pinger
		redis          Pinger
		wantStatusCode int
		wantSuccess    bool
	}{
		{
			name:           "TestHealthChecker_AllUp",
			db:             &fakePinger{err: nil},
			redis:          &fakePinger{err: nil},
			wantStatusCode: http.StatusOK,
			wantSuccess:    true,
		},
		{
			name:           "TestHealthChecker_DBDown",
			db:             &fakePinger{err: errors.New("db connection timeout")},
			redis:          &fakePinger{err: nil},
			wantStatusCode: http.StatusServiceUnavailable,
			wantSuccess:    false,
		},
		{
			name:           "TestHealthChecker_RedisDown",
			db:             &fakePinger{err: nil},
			redis:          &fakePinger{err: errors.New("redis connection refused")},
			wantStatusCode: http.StatusServiceUnavailable,
			wantSuccess:    false,
		},
		{
			name:           "TestHealthChecker_BothDown",
			db:             &fakePinger{err: errors.New("db down")},
			redis:          &fakePinger{err: errors.New("redis down")},
			wantStatusCode: http.StatusServiceUnavailable,
			wantSuccess:    false,
		},
		{
			name:           "TestHealthChecker_NilPingers",
			db:             nil,
			redis:          nil,
			wantStatusCode: http.StatusServiceUnavailable,
			wantSuccess:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := &HealthChecker{DB: tt.db, Redis: tt.redis}
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()

			hc.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			var resp map[string]any
			err := json.Unmarshal(rec.Body.Bytes(), &resp)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSuccess, resp["success"])
		})
	}
}

type fakeRedisClient struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newFakeRedisClient() *fakeRedisClient {
	return &fakeRedisClient{counts: make(map[string]int64)}
}

func (f *fakeRedisClient) Incr(ctx context.Context, key string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[key]++
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(f.counts[key])
	return cmd
}

func (f *fakeRedisClient) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(true)
	return cmd
}

func TestNewRouterWithLimiter_RateLimitAppliesToAuthenticatedRoute(t *testing.T) {
	jwtSvc := auth.NewJWTService("router-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
	middleware := auth.NewAuthMiddleware(jwtSvc)

	userID := uuid.New()
	tenantID := uuid.New()

	authUCMock := authmocks.NewMockAuthUseCase(t)
	authUCMock.EXPECT().GetMe(mock.Anything, tenantID, userID).Return(&domain.User{
		ID:       userID,
		TenantID: tenantID,
		Email:    "admin@flowforge.local",
		Role:     "admin",
	}, nil)

	authHandler := auth.NewAuthHandler(authUCMock)
	fakeRdb := newFakeRedisClient()
	limiter := ratelimit.NewLimiter(fakeRdb, nil)

	router := NewRouterWithLimiter(nil, authHandler, nil, nil, nil, middleware, nil, limiter, false)

	pair, err := jwtSvc.GenerateTokenPair(userID, tenantID, uuid.Nil, "admin@flowforge.local", "admin")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	expectedKey := "ratelimit:tenant:" + tenantID.String() + ":general"
	fakeRdb.mu.Lock()
	incrCount := fakeRdb.counts[expectedKey]
	fakeRdb.mu.Unlock()

	assert.Equal(t, int64(1), incrCount, "Authenticated general route must invoke rate limiter with tenant key")
}

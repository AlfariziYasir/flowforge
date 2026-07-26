package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
)

func TestAuthMiddleware(t *testing.T) {
	secret := "middleware-test-secret"
	jwtSvc := auth.NewJWTService(secret, 15*time.Minute, 7*24*time.Hour)
	middleware := auth.NewAuthMiddleware(jwtSvc)

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.AuthUserFromContext(r.Context())
		if !ok {
			http.Error(w, "missing auth user", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(user.Email))
	})

	t.Run("returns 401 Unauthorized when Authorization header is missing", func(t *testing.T) {
		is := assert.New(t)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		rec := httptest.NewRecorder()

		middleware.Authenticate(dummyHandler).ServeHTTP(rec, req)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})

	t.Run("returns 401 Unauthorized when Authorization format is invalid", func(t *testing.T) {
		is := assert.New(t)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		rec := httptest.NewRecorder()

		middleware.Authenticate(dummyHandler).ServeHTTP(rec, req)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})

	t.Run("returns 401 Unauthorized when token is invalid or expired", func(t *testing.T) {
		is := assert.New(t)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		req.Header.Set("Authorization", "Bearer invalid.jwt.token")
		rec := httptest.NewRecorder()

		middleware.Authenticate(dummyHandler).ServeHTTP(rec, req)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})

	t.Run("injects AuthUser into context and calls next handler on valid token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userID := uuid.New()
		tenantID := uuid.New()
		email := "user@flowforge.local"
		role := "editor"

		pair, err := jwtSvc.GenerateTokenPair(userID, tenantID, email, role)
		req.NoError(err)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		httpReq.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rec := httptest.NewRecorder()

		middleware.Authenticate(dummyHandler).ServeHTTP(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)
		is.Contains(rec.Body.String(), email)
	})

	t.Run("returns 401 Unauthorized when token JTI is blacklisted", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		blacklist := newMockBlacklistStore()
		mwWithBlacklist := auth.NewAuthMiddlewareWithBlacklist(jwtSvc, blacklist)

		pair, err := jwtSvc.GenerateTokenPair(uuid.New(), uuid.New(), "user@flowforge.local", "editor")
		req.NoError(err)

		claims, err := jwtSvc.ValidateAccessToken(pair.AccessToken)
		req.NoError(err)

		err = blacklist.Revoke(context.Background(), claims.JTI, 15*time.Minute)
		req.NoError(err)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
		httpReq.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		rec := httptest.NewRecorder()

		mwWithBlacklist.Authenticate(dummyHandler).ServeHTTP(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})
}

func TestRequireRole(t *testing.T) {
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("allows user with matching role", func(t *testing.T) {
		is := assert.New(t)

		user := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "editor@flowforge.local",
			Role:     "editor",
		}
		ctx := auth.ContextWithAuthUser(context.Background(), user)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		auth.RequireRole("admin", "editor")(dummyHandler).ServeHTTP(rec, req)

		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("returns 403 Forbidden for user with unallowed role", func(t *testing.T) {
		is := assert.New(t)

		user := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "viewer@flowforge.local",
			Role:     "viewer",
		}
		ctx := auth.ContextWithAuthUser(context.Background(), user)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		auth.RequireRole("admin", "editor")(dummyHandler).ServeHTTP(rec, req)

		is.Equal(http.StatusForbidden, rec.Code)
	})

	t.Run("returns 401 Unauthorized when context lacks AuthUser", func(t *testing.T) {
		is := assert.New(t)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil)
		rec := httptest.NewRecorder()

		auth.RequireRole("admin")(dummyHandler).ServeHTTP(rec, req)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})
}

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flowforge/internal/auth"
	"flowforge/internal/platform/config"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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

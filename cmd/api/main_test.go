package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flowforge/internal/auth"
	"flowforge/internal/platform/config"
	"flowforge/internal/workflow"
	workflowmocks "flowforge/internal/workflow/mocks"

	"github.com/google/uuid"
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

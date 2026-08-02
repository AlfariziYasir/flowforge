package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"flowforge/internal/auth"
	authmocks "flowforge/internal/auth/mocks"
	"flowforge/internal/domain"
	"flowforge/internal/tenant"
	tenantmocks "flowforge/internal/tenant/mocks"
)

func TestAuthHandler_Login(t *testing.T) {
	tnt := &domain.Tenant{
		ID:   uuid.New(),
		Slug: "default-tenant",
		Name: "Default Tenant",
	}

	passSvc := auth.NewPasswordServiceWithCost(bcrypt.MinCost)
	hashedPass, _ := passSvc.HashPassword("SecretP@ss123")
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tnt.ID,
		Email:        "admin@flowforge.local",
		PasswordHash: hashedPass,
		Role:         "admin",
		IsActive:     true,
	}

	t.Run("returns token pair and user profile on valid credentials", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantRepo := tenantmocks.NewMockTenantRepository(t)
		tenantRepo.EXPECT().FindBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "admin@flowforge.local").Return(user, nil)

		tenantUC := tenant.NewTenantUseCase(tenantRepo)
		jwtSvc := auth.NewJWTService("handler-test-secret-32chars-long!!", 15*time.Minute, 7*24*time.Hour)
		blacklist := auth.NewNoopTokenBlacklist()
		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().CreateSession(mock.Anything, mock.Anything, 7*24*time.Hour).Return(nil)
		authUC := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)
		handler := auth.NewAuthHandler(authUC)

		bodyBytes, _ := json.Marshal(map[string]string{
			"tenantSlug": "default-tenant",
			"email":      "admin@flowforge.local",
			"password":   "SecretP@ss123",
		})

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.Login(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		req.NoError(err)
		is.NotEmpty(resp["accessToken"])
		is.NotEmpty(resp["refreshToken"])
		is.NotNil(resp["user"])
	})

	t.Run("returns 401 Unauthorized on invalid password", func(t *testing.T) {
		is := assert.New(t)

		tenantRepo := tenantmocks.NewMockTenantRepository(t)
		tenantRepo.EXPECT().FindBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "admin@flowforge.local").Return(user, nil)

		tenantUC := tenant.NewTenantUseCase(tenantRepo)
		jwtSvc := auth.NewJWTService("handler-test-secret-32chars-long!!", 15*time.Minute, 7*24*time.Hour)
		blacklist := auth.NewNoopTokenBlacklist()
		sessionStore := auth.NewNoopSessionStore()
		authUC := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)
		handler := auth.NewAuthHandler(authUC)

		bodyBytes, _ := json.Marshal(map[string]string{
			"tenantSlug": "default-tenant",
			"email":      "admin@flowforge.local",
			"password":   "WrongPassword",
		})

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
		rec := httptest.NewRecorder()

		handler.Login(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})

	t.Run("returns 401 Unauthorized when tenant slug does not exist", func(t *testing.T) {
		is := assert.New(t)

		tenantRepo := tenantmocks.NewMockTenantRepository(t)
		tenantRepo.EXPECT().FindBySlug(mock.Anything, "nonexistent-tenant").Return(nil, tenant.ErrTenantNotFound)

		userRepo := authmocks.NewMockUserRepository(t)

		tenantUC := tenant.NewTenantUseCase(tenantRepo)
		jwtSvc := auth.NewJWTService("handler-test-secret-32chars-long!!", 15*time.Minute, 7*24*time.Hour)
		blacklist := auth.NewNoopTokenBlacklist()
		sessionStore := auth.NewNoopSessionStore()
		authUC := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)
		handler := auth.NewAuthHandler(authUC)

		bodyBytes, _ := json.Marshal(map[string]string{
			"tenantSlug": "nonexistent-tenant",
			"email":      "admin@flowforge.local",
			"password":   "SecretP@ss123",
		})

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
		rec := httptest.NewRecorder()

		handler.Login(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})

	t.Run("uniform failure response for all login failure reasons", func(t *testing.T) {
		is := assert.New(t)

		mockUC := authmocks.NewMockAuthUseCase(t)
		mockUC.EXPECT().Login(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, auth.ErrUnauthorized)

		handler := auth.NewAuthHandler(mockUC)

		bodyBytes, _ := json.Marshal(map[string]string{
			"tenantSlug": "tenant",
			"email":      "user@flowforge.local",
			"password":   "password",
		})

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
		rec := httptest.NewRecorder()

		handler.Login(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
		is.Contains(rec.Body.String(), "invalid tenant, email, or password")
	})
}

func TestAuthHandler_GetMe(t *testing.T) {
	t.Run("returns authenticated user profile from context and database", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		user := &domain.User{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "admin@flowforge.local",
			Role:     "admin",
			IsActive: true,
		}

		mockUC := authmocks.NewMockAuthUseCase(t)
		mockUC.EXPECT().GetMe(mock.Anything, user.TenantID, user.ID).Return(user, nil)

		handler := auth.NewAuthHandler(mockUC)

		authUser := auth.AuthUser{
			ID:       user.ID,
			TenantID: user.TenantID,
			Email:    user.Email,
			Role:     user.Role,
		}
		ctx := auth.ContextWithAuthUser(context.Background(), authUser)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.GetMe(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		req.NoError(err)
		is.Equal(user.Email, resp["email"])
		is.Equal(user.Role, resp["role"])
	})

	t.Run("returns 404 Not Found when user no longer exists in database", func(t *testing.T) {
		is := assert.New(t)

		authUser := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "deleted@flowforge.local",
			Role:     "viewer",
		}
		ctx := auth.ContextWithAuthUser(context.Background(), authUser)

		mockUC := authmocks.NewMockAuthUseCase(t)
		mockUC.EXPECT().GetMe(mock.Anything, authUser.TenantID, authUser.ID).Return(nil, auth.ErrUserNotFound)

		handler := auth.NewAuthHandler(mockUC)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.GetMe(rec, httpReq)

		is.Equal(http.StatusNotFound, rec.Code)
	})
}

func TestAuthHandler_Logout(t *testing.T) {
	t.Run("returns success envelope on logout", func(t *testing.T) {
		is := assert.New(t)

		mockUC := authmocks.NewMockAuthUseCase(t)
		mockUC.EXPECT().Logout(mock.Anything, mock.Anything).Return(nil)

		handler := auth.NewAuthHandler(mockUC)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		rec := httptest.NewRecorder()

		handler.Logout(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)
	})
}

func TestAuthHandler_LogoutAll(t *testing.T) {
	t.Run("successfully logs out all devices for authenticated user", func(t *testing.T) {
		is := assert.New(t)

		tenantID := uuid.New()
		userID := uuid.New()

		mockUC := authmocks.NewMockAuthUseCase(t)
		mockUC.EXPECT().LogoutAllDevices(mock.Anything, tenantID, userID).Return(nil)

		handler := auth.NewAuthHandler(mockUC)

		authUser := auth.AuthUser{
			ID:       userID,
			TenantID: tenantID,
			Email:    "user@flowforge.local",
			Role:     "admin",
		}
		ctx := auth.ContextWithAuthUser(context.Background(), authUser)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout-all", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.LogoutAll(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("returns 401 Unauthorized when auth context is missing", func(t *testing.T) {
		is := assert.New(t)

		mockUC := authmocks.NewMockAuthUseCase(t)
		handler := auth.NewAuthHandler(mockUC)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout-all", nil)
		rec := httptest.NewRecorder()

		handler.LogoutAll(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})
}

func TestAuthHandler_ListSessions(t *testing.T) {
	t.Run("returns paginated sessions list for authenticated user", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantID := uuid.New()
		userID := uuid.New()

		paginatedRes := &auth.PaginatedSessions{
			Sessions: []*auth.UserSession{
				{SessionID: uuid.New(), UserID: userID, TenantID: tenantID},
			},
			Total: 1,
			Page:  1,
			Size:  20,
		}

		mockUC := authmocks.NewMockAuthUseCase(t)
		mockUC.EXPECT().ListSessions(mock.Anything, tenantID, userID, 1, 20).Return(paginatedRes, nil)

		handler := auth.NewAuthHandler(mockUC)

		authUser := auth.AuthUser{
			ID:       userID,
			TenantID: tenantID,
			Email:    "user@flowforge.local",
			Role:     "admin",
		}
		ctx := auth.ContextWithAuthUser(context.Background(), authUser)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sessions?page=1&pageSize=20", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.ListSessions(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		req.NoError(err)
		is.Equal(float64(1), resp["total"])
	})

	t.Run("returns 401 Unauthorized when auth context is missing", func(t *testing.T) {
		is := assert.New(t)

		mockUC := authmocks.NewMockAuthUseCase(t)
		handler := auth.NewAuthHandler(mockUC)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
		rec := httptest.NewRecorder()

		handler.ListSessions(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
	})
}

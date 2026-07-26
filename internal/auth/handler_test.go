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
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
	"flowforge/internal/domain"
	"flowforge/internal/tenant"
)

type MockTenantRepository struct {
	tenants map[string]*domain.Tenant
}

func NewMockTenantRepository() *MockTenantRepository {
	return &MockTenantRepository{
		tenants: make(map[string]*domain.Tenant),
	}
}

func (m *MockTenantRepository) FindBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	t, exists := m.tenants[slug]
	if !exists {
		return nil, tenant.ErrTenantNotFound
	}
	return t, nil
}

func (m *MockTenantRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	for _, t := range m.tenants {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, tenant.ErrTenantNotFound
}

func (m *MockTenantRepository) Save(ctx context.Context, t *domain.Tenant) error {
	m.tenants[t.Slug] = t
	return nil
}



func setupHandlerTest() (*auth.AuthHandler, *MockTenantRepository, *MockUserRepository, auth.JWTService, auth.PasswordService) {
	tenantRepo := NewMockTenantRepository()
	userRepo := NewMockUserRepository()
	jwtSvc := auth.NewJWTService("handler-test-secret", 15*time.Minute, 7*24*time.Hour)
	passSvc := auth.NewPasswordService()

	handler := auth.NewAuthHandler(tenantRepo, userRepo, jwtSvc, passSvc)
	return handler, tenantRepo, userRepo, jwtSvc, passSvc
}

func TestAuthHandler_Login(t *testing.T) {
	handler, tenantRepo, userRepo, _, passSvc := setupHandlerTest()
	ctx := context.Background()

	tnt := &domain.Tenant{
		ID:        uuid.New(),
		Slug:      "default-tenant",
		Name:      "Default Tenant",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = tenantRepo.Save(ctx, tnt)

	hashedPass, _ := passSvc.HashPassword("SecretP@ss123")
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tnt.ID,
		Email:        "admin@flowforge.local",
		PasswordHash: hashedPass,
		Role:         "admin",
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	_ = userRepo.CreateUser(ctx, user)

	t.Run("returns token pair and user profile on valid credentials", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

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

	t.Run("successfully logs in with uppercase email and tenant slug", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		bodyBytes, _ := json.Marshal(map[string]string{
			"tenantSlug": "DEFAULT-TENANT ",
			"email":      " ADMIN@FLOWFORGE.LOCAL ",
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
	})

	t.Run("returns 401 Unauthorized on invalid password", func(t *testing.T) {
		is := assert.New(t)

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
}

func TestAuthHandler_Refresh(t *testing.T) {
	handler, tenantRepo, userRepo, jwtSvc, passSvc := setupHandlerTest()
	ctx := context.Background()

	tnt := &domain.Tenant{ID: uuid.New(), Slug: "default-tenant"}
	_ = tenantRepo.Save(ctx, tnt)

	hashedPass, _ := passSvc.HashPassword("SecretP@ss123")
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tnt.ID,
		Email:        "admin@flowforge.local",
		PasswordHash: hashedPass,
		Role:         "admin",
		IsActive:     true,
	}
	_ = userRepo.CreateUser(ctx, user)

	t.Run("returns new token pair given valid refresh token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		pair, err := jwtSvc.GenerateTokenPair(user.ID, tnt.ID, user.Email, user.Role)
		req.NoError(err)

		bodyBytes, _ := json.Marshal(map[string]string{
			"refreshToken": pair.RefreshToken,
		})

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewReader(bodyBytes))
		rec := httptest.NewRecorder()

		handler.Refresh(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err = json.Unmarshal(rec.Body.Bytes(), &resp)
		req.NoError(err)
		is.NotEmpty(resp["accessToken"])
		is.NotEmpty(resp["refreshToken"])
	})
}

func TestAuthHandler_GetMe(t *testing.T) {
	handler, _, _, _, _ := setupHandlerTest()

	t.Run("returns authenticated user profile from context", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		authUser := auth.AuthUser{
			ID:       uuid.New(),
			TenantID: uuid.New(),
			Email:    "admin@flowforge.local",
			Role:     "admin",
		}
		ctx := auth.ContextWithAuthUser(context.Background(), authUser)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil).WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.GetMe(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		req.NoError(err)
		is.Equal(authUser.Email, resp["email"])
		is.Equal(authUser.Role, resp["role"])
	})
}

func TestAuthHandler_Logout(t *testing.T) {
	handler, _, _, _, _ := setupHandlerTest()

	t.Run("returns success envelope on logout", func(t *testing.T) {
		is := assert.New(t)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		rec := httptest.NewRecorder()

		handler.Logout(rec, httpReq)

		is.Equal(http.StatusOK, rec.Code)
	})
}

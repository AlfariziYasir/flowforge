package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
	authmocks "flowforge/internal/auth/mocks"
	"flowforge/internal/domain"
	"flowforge/internal/platform/httpx"
)

func TestUserHandler_CreateUser(t *testing.T) {
	tenantID := uuid.New()
	adminUser := auth.AuthUser{
		ID:       uuid.New(),
		TenantID: tenantID,
		Email:    "admin@flowforge.local",
		Role:     "admin",
	}

	t.Run("successfully creates user with status 201", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		createdUser := &domain.User{
			ID:       uuid.New(),
			TenantID: tenantID,
			Email:    "new@flowforge.local",
			Role:     "editor",
			IsActive: true,
		}

		userUC.EXPECT().CreateUser(mock.Anything, mock.MatchedBy(func(cmd auth.CreateUserCommand) bool {
			return cmd.TenantID == tenantID && cmd.Email == "new@flowforge.local" && cmd.Role == "editor"
		})).Return(createdUser, nil)

		body := map[string]string{
			"email":    "new@flowforge.local",
			"password": "SecretPassword123",
			"role":     "editor",
		}
		jsonBytes, _ := json.Marshal(body)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(jsonBytes))
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.CreateUser(rec, httpReq.WithContext(ctx))

		req.Equal(http.StatusCreated, rec.Code)
		var env httpx.Envelope
		req.NoError(json.Unmarshal(rec.Body.Bytes(), &env))
		is.True(env.Success)
		is.Contains(rec.Body.String(), "new@flowforge.local")
	})

	t.Run("returns 401 Unauthorized when auth context is missing", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
		rec := httptest.NewRecorder()

		handler.CreateUser(rec, httpReq)

		is.Equal(http.StatusUnauthorized, rec.Code)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		is.False(env.Success)
		is.Equal(httpx.CodeAuthUnauthorized, env.Error.Code)
	})

	t.Run("returns 409 Conflict when user email already exists", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		userUC.EXPECT().CreateUser(mock.Anything, mock.Anything).Return(nil, auth.ErrUserAlreadyExists)

		body := map[string]string{
			"email":    "existing@flowforge.local",
			"password": "SecretPassword123",
			"role":     "editor",
		}
		jsonBytes, _ := json.Marshal(body)

		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(jsonBytes))
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.CreateUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusConflict, rec.Code)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		is.False(env.Success)
		is.Equal(httpx.CodeConflict, env.Error.Code)
	})
}

func TestUserHandler_ListUsers(t *testing.T) {
	tenantID := uuid.New()
	adminUser := auth.AuthUser{
		ID:       uuid.New(),
		TenantID: tenantID,
		Email:    "admin@flowforge.local",
		Role:     "admin",
	}

	t.Run("successfully lists users", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		paginatedResult := &auth.PaginatedUsers{
			Users: []*domain.User{
				{ID: uuid.New(), TenantID: tenantID, Email: "user1@flowforge.local"},
			},
			Total: 1,
			Page:  1,
			Size:  20,
		}

		userUC.EXPECT().ListUsers(mock.Anything, mock.Anything).Return(paginatedResult, nil)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users?page=1&pageSize=20", nil)
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.ListUsers(rec, httpReq.WithContext(ctx))

		req.Equal(http.StatusOK, rec.Code)
		var env httpx.Envelope
		req.NoError(json.Unmarshal(rec.Body.Bytes(), &env))
		is.True(env.Success)
		is.Contains(rec.Body.String(), "user1@flowforge.local")
	})
}

func TestUserHandler_GetUser(t *testing.T) {
	tenantID := uuid.New()
	targetID := uuid.New()
	adminUser := auth.AuthUser{
		ID:       uuid.New(),
		TenantID: tenantID,
		Email:    "admin@flowforge.local",
		Role:     "admin",
	}

	t.Run("returns user by id", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		targetUser := &domain.User{
			ID:       targetID,
			TenantID: tenantID,
			Email:    "target@flowforge.local",
		}

		userUC.EXPECT().GetUser(mock.Anything, tenantID, targetID).Return(targetUser, nil)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+targetID.String(), nil)
		httpReq.SetPathValue("userId", targetID.String())
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.GetUser(rec, httpReq.WithContext(ctx))

		req.Equal(http.StatusOK, rec.Code)
		is.Contains(rec.Body.String(), "target@flowforge.local")
	})

	t.Run("returns 404 Not Found when user does not exist", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		userUC.EXPECT().GetUser(mock.Anything, tenantID, targetID).Return(nil, auth.ErrUserNotFound)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+targetID.String(), nil)
		httpReq.SetPathValue("userId", targetID.String())
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.GetUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusNotFound, rec.Code)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		is.False(env.Success)
		is.Equal(httpx.CodeNotFound, env.Error.Code)
	})

	t.Run("returns 400 Bad Request on invalid uuid format", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/users/invalid-uuid", nil)
		httpReq.SetPathValue("userId", "invalid-uuid")
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.GetUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusBadRequest, rec.Code)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		is.False(env.Success)
		is.Equal(httpx.CodeInvalidRequestBody, env.Error.Code)
	})
}

func TestUserHandler_UpdateUser(t *testing.T) {
	tenantID := uuid.New()
	adminUser := auth.AuthUser{
		ID:       uuid.New(),
		TenantID: tenantID,
		Email:    "admin@flowforge.local",
		Role:     "admin",
	}

	t.Run("returns 403 Forbidden when admin tries to deactivate self", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		userUC.EXPECT().UpdateUser(mock.Anything, mock.Anything).Return(nil, auth.ErrCannotDeactivateSelf)

		body := map[string]interface{}{"isActive": false}
		jsonBytes, _ := json.Marshal(body)

		httpReq := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+adminUser.ID.String(), bytes.NewReader(jsonBytes))
		httpReq.SetPathValue("userId", adminUser.ID.String())
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.UpdateUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusForbidden, rec.Code)
	})

	t.Run("returns 409 Conflict when updated user email already exists in tenant", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		userUC.EXPECT().UpdateUser(mock.Anything, mock.Anything).Return(nil, auth.ErrUserAlreadyExists)

		targetID := uuid.New()
		body := map[string]interface{}{"email": "existing@flowforge.local"}
		jsonBytes, _ := json.Marshal(body)

		httpReq := httptest.NewRequest(http.MethodPatch, "/api/v1/users/"+targetID.String(), bytes.NewReader(jsonBytes))
		httpReq.SetPathValue("userId", targetID.String())
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.UpdateUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusConflict, rec.Code)
	})
}

func TestUserHandler_DeleteUser(t *testing.T) {
	tenantID := uuid.New()
	targetID := uuid.New()
	adminUser := auth.AuthUser{
		ID:       uuid.New(),
		TenantID: tenantID,
		Email:    "admin@flowforge.local",
		Role:     "admin",
	}

	t.Run("successfully deletes user with status 200", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		userUC.EXPECT().DeleteUser(mock.Anything, adminUser.ID, tenantID, targetID).Return(nil)

		httpReq := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+targetID.String(), nil)
		httpReq.SetPathValue("userId", targetID.String())
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.DeleteUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusOK, rec.Code)
	})

	t.Run("returns 403 Forbidden when admin tries to delete self", func(t *testing.T) {
		is := assert.New(t)

		userUC := authmocks.NewMockUserUseCase(t)
		handler := auth.NewUserHandler(userUC)

		userUC.EXPECT().DeleteUser(mock.Anything, adminUser.ID, tenantID, adminUser.ID).Return(auth.ErrCannotDeleteSelf)

		httpReq := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+adminUser.ID.String(), nil)
		httpReq.SetPathValue("userId", adminUser.ID.String())
		ctx := auth.ContextWithAuthUser(httpReq.Context(), adminUser)
		rec := httptest.NewRecorder()

		handler.DeleteUser(rec, httpReq.WithContext(ctx))

		is.Equal(http.StatusForbidden, rec.Code)
	})
}

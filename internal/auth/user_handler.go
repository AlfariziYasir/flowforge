package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// UserHandler handles HTTP transport for user management.
type UserHandler struct {
	userUC UserUseCase
}

// NewUserHandler creates a new UserHandler instance.
func NewUserHandler(userUC UserUseCase) *UserHandler {
	return &UserHandler{
		userUC: userUC,
	}
}

// CreateUser handles POST /api/v1/users.
func (h *UserHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}

	cmd := CreateUserCommand{
		TenantID: authUser.TenantID,
		Email:    req.Email,
		Password: req.Password,
		Role:     req.Role,
	}

	user, err := h.userUC.CreateUser(r.Context(), cmd)
	if err != nil {
		if errors.Is(err, ErrUserAlreadyExists) {
			respondJSONError(w, http.StatusConflict, "Conflict", "user email already exists in tenant")
			return
		}
		if errors.Is(err, ErrInvalidPasswordLength) || errors.Is(err, ErrInvalidRole) {
			respondJSONError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to create user")
		return
	}

	respondJSON(w, http.StatusCreated, user)
}

// ListUsers handles GET /api/v1/users.
func (h *UserHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	queryVals := r.URL.Query()
	page, _ := strconv.Atoi(queryVals.Get("page"))
	pageSize, _ := strconv.Atoi(queryVals.Get("pageSize"))
	role := queryVals.Get("role")
	orderBy := queryVals.Get("orderBy")

	var activeOnly *bool
	if activeStr := queryVals.Get("activeOnly"); activeStr != "" {
		if val, err := strconv.ParseBool(activeStr); err == nil {
			activeOnly = &val
		}
	}

	var isAsc *bool
	if ascStr := queryVals.Get("isAsc"); ascStr != "" {
		if val, err := strconv.ParseBool(ascStr); err == nil {
			isAsc = &val
		}
	}

	query := ListUsersQuery{
		TenantID:   authUser.TenantID,
		Page:       page,
		PageSize:   pageSize,
		OrderBy:    orderBy,
		Role:       role,
		IsAsc:      isAsc,
		ActiveOnly: activeOnly,
	}

	res, err := h.userUC.ListUsers(r.Context(), query)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to list users")
		return
	}

	respondJSON(w, http.StatusOK, res)
}

// GetUser handles GET /api/v1/users/{userId}.
func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	userIDStr := r.PathValue("userId")
	targetID, err := uuid.Parse(userIDStr)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid user ID format")
		return
	}

	user, err := h.userUC.GetUser(r.Context(), authUser.TenantID, targetID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			respondJSONError(w, http.StatusNotFound, "Not Found", "user not found")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to retrieve user")
		return
	}

	respondJSON(w, http.StatusOK, user)
}

// UpdateUser handles PATCH /api/v1/users/{userId}.
func (h *UserHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	userIDStr := r.PathValue("userId")
	targetID, err := uuid.Parse(userIDStr)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid user ID format")
		return
	}

	var req struct {
		Email    *string `json:"email,omitempty"`
		Role     *string `json:"role,omitempty"`
		IsActive *bool   `json:"isActive,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}

	cmd := UpdateUserCommand{
		ActorID:  authUser.ID,
		TenantID: authUser.TenantID,
		UserID:   targetID,
		Email:    req.Email,
		Role:     req.Role,
		IsActive: req.IsActive,
	}

	user, err := h.userUC.UpdateUser(r.Context(), cmd)
	if err != nil {
		if errors.Is(err, ErrUserAlreadyExists) {
			respondJSONError(w, http.StatusConflict, "Conflict", "user email already exists in tenant")
			return
		}
		if errors.Is(err, ErrCannotDeactivateSelf) {
			respondJSONError(w, http.StatusForbidden, "Forbidden", err.Error())
			return
		}
		if errors.Is(err, ErrUserNotFound) {
			respondJSONError(w, http.StatusNotFound, "Not Found", "user not found")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to update user")
		return
	}

	respondJSON(w, http.StatusOK, user)
}

// DeleteUser handles DELETE /api/v1/users/{userId}.
func (h *UserHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	userIDStr := r.PathValue("userId")
	targetID, err := uuid.Parse(userIDStr)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid user ID format")
		return
	}

	if err := h.userUC.DeleteUser(r.Context(), authUser.ID, authUser.TenantID, targetID); err != nil {
		if errors.Is(err, ErrCannotDeleteSelf) {
			respondJSONError(w, http.StatusForbidden, "Forbidden", err.Error())
			return
		}
		if errors.Is(err, ErrUserNotFound) {
			respondJSONError(w, http.StatusNotFound, "Not Found", "user not found")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to delete user")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "user deleted successfully",
	})
}

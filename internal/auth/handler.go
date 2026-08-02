package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type LoginRequest struct {
	TenantSlug string `json:"tenantSlug"`
	Email      string `json:"email"`
	Password   string `json:"password"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type AuthHandler struct {
	authUC     AuthUseCase
	trustProxy bool
}

func NewAuthHandler(authUC AuthUseCase) *AuthHandler {
	return &AuthHandler{
		authUC: authUC,
	}
}

func NewAuthHandlerWithTrustProxy(authUC AuthUseCase, trustProxy bool) *AuthHandler {
	return &AuthHandler{
		authUC:     authUC,
		trustProxy: trustProxy,
	}
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}

	if req.TenantSlug == "" || req.Email == "" || req.Password == "" {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "tenantSlug, email, and password are required")
		return
	}

	ipAddress := r.RemoteAddr
	if h.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.Index(xff, ","); idx != -1 {
				ipAddress = strings.TrimSpace(xff[:idx])
			} else {
				ipAddress = strings.TrimSpace(xff)
			}
		}
	}
	userAgent := r.UserAgent()

	res, err := h.authUC.Login(r.Context(), req.TenantSlug, req.Email, req.Password, ipAddress, userAgent)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid tenant, email, or password")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to process login")
		return
	}

	respondJSON(w, http.StatusOK, res)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}

	if req.RefreshToken == "" {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "refreshToken is required")
		return
	}

	res, err := h.authUC.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrUserInactive) {
			respondJSONError(w, http.StatusForbidden, "Forbidden", "user account is inactive")
			return
		}
		if errors.Is(err, ErrUnauthorized) {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired refresh token")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to refresh token")
		return
	}

	respondJSON(w, http.StatusOK, res)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	tokenStr := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))

	if err := h.authUC.Logout(r.Context(), tokenStr); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or missing token")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to process logout")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "logged out successfully",
	})
}

func (h *AuthHandler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	if err := h.authUC.LogoutAllDevices(r.Context(), authUser.TenantID, authUser.ID); err != nil {
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to logout all devices")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "all devices logged out successfully",
	})
}

func (h *AuthHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	page := 1
	pageSize := 20

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}
	if sizeStr := r.URL.Query().Get("pageSize"); sizeStr != "" {
		if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 {
			pageSize = s
		}
	}

	res, err := h.authUC.ListSessions(r.Context(), authUser.TenantID, authUser.ID, page, pageSize)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to list user sessions")
		return
	}

	respondJSON(w, http.StatusOK, res)
}

func (h *AuthHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	user, err := h.authUC.GetMe(r.Context(), authUser.TenantID, authUser.ID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			respondJSONError(w, http.StatusNotFound, "Not Found", "user account no longer exists")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to retrieve profile")
		return
	}

	respondJSON(w, http.StatusOK, user)
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

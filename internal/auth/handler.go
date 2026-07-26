package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"flowforge/internal/domain"
	"flowforge/internal/tenant"
)

// Dummy hash used for constant-time comparison when user or tenant is not found (timing attack defense).
const dummyBcryptHash = "$2a$12$e0M2/h3uA.g.0Sg3/7aG9u4nU6b3e.6V1s/8c7e.6V1s/8c7e.6V1"

// LoginRequest defines payload for POST /api/v1/auth/login.
type LoginRequest struct {
	TenantSlug string `json:"tenantSlug"`
	Email      string `json:"email"`
	Password   string `json:"password"`
}

// RefreshRequest defines payload for POST /api/v1/auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// AuthResponse defines response for login and token refresh.
type AuthResponse struct {
	AccessToken  string       `json:"accessToken"`
	RefreshToken string       `json:"refreshToken"`
	ExpiresIn    int64        `json:"expiresIn"`
	User         *domain.User `json:"user"`
}

// AuthHandler handles HTTP endpoints for identity and authentication.
type AuthHandler struct {
	tenantRepo tenant.TenantRepository
	userRepo   UserRepository
	jwtService JWTService
	passSvc    PasswordService
	blacklist  TokenBlacklist
}

// NewAuthHandler creates a new AuthHandler instance with default noop blacklist.
func NewAuthHandler(
	tenantRepo tenant.TenantRepository,
	userRepo UserRepository,
	jwtService JWTService,
	passSvc PasswordService,
) *AuthHandler {
	return NewAuthHandlerWithBlacklist(tenantRepo, userRepo, jwtService, passSvc, NewNoopTokenBlacklist())
}

// NewAuthHandlerWithBlacklist creates an AuthHandler with custom TokenBlacklist.
func NewAuthHandlerWithBlacklist(
	tenantRepo tenant.TenantRepository,
	userRepo UserRepository,
	jwtService JWTService,
	passSvc PasswordService,
	blacklist TokenBlacklist,
) *AuthHandler {
	if blacklist == nil {
		blacklist = NewNoopTokenBlacklist()
	}
	return &AuthHandler{
		tenantRepo: tenantRepo,
		userRepo:   userRepo,
		jwtService: jwtService,
		passSvc:    passSvc,
		blacklist:  blacklist,
	}
}

// Login handles user authentication and returns JWT tokens.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}

	req.TenantSlug = strings.ToLower(strings.TrimSpace(req.TenantSlug))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if req.TenantSlug == "" || req.Email == "" || req.Password == "" {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "tenantSlug, email, and password are required")
		return
	}

	tnt, err := h.tenantRepo.FindBySlug(r.Context(), req.TenantSlug)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			_ = h.passSvc.ComparePassword(dummyBcryptHash, req.Password)
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid tenant, email, or password")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to lookup tenant")
		return
	}

	user, err := h.userRepo.FindByEmail(r.Context(), tnt.ID, req.Email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			_ = h.passSvc.ComparePassword(dummyBcryptHash, req.Password)
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid tenant, email, or password")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to lookup user")
		return
	}

	if !user.IsActive {
		_ = h.passSvc.ComparePassword(dummyBcryptHash, req.Password)
		respondJSONError(w, http.StatusForbidden, "Forbidden", "user account is inactive")
		return
	}

	if err := h.passSvc.ComparePassword(user.PasswordHash, req.Password); err != nil {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid tenant, email, or password")
		return
	}

	pair, err := h.jwtService.GenerateTokenPair(user.ID, user.TenantID, user.Email, user.Role)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to generate authentication tokens")
		return
	}

	respondJSON(w, http.StatusOK, AuthResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		User:         user,
	})
}

// Refresh handles token refresh using a valid refresh token and performs token rotation.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}

	if req.RefreshToken == "" {
		respondJSONError(w, http.StatusBadRequest, "Bad Request", "refreshToken is required")
		return
	}

	claims, err := h.jwtService.ValidateRefreshToken(req.RefreshToken)
	if err != nil {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired refresh token")
		return
	}

	if claims.JTI != "" {
		revoked, err := h.blacklist.IsRevoked(r.Context(), claims.JTI)
		if err == nil && revoked {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "refresh token has been revoked")
			return
		}
	}

	user, err := h.userRepo.FindByID(r.Context(), claims.TenantID, claims.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "user not found")
			return
		}
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to lookup user")
		return
	}

	if !user.IsActive {
		respondJSONError(w, http.StatusForbidden, "Forbidden", "user account is inactive")
		return
	}

	pair, err := h.jwtService.GenerateTokenPair(user.ID, user.TenantID, user.Email, user.Role)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to generate tokens")
		return
	}

	// Revoke old refresh token (token rotation)
	if claims.JTI != "" {
		_ = h.blacklist.Revoke(r.Context(), claims.JTI, 7*24*time.Hour)
	}

	respondJSON(w, http.StatusOK, AuthResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		User:         user,
	})
}

// Logout revokes the current access token / refresh token JTI.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if claims, err := h.jwtService.ValidateAccessToken(parts[1]); err == nil && claims.JTI != "" {
				_ = h.blacklist.Revoke(r.Context(), claims.JTI, 24*time.Hour)
			}
		}
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "logged out successfully",
	})
}

// GetMe returns the authenticated user profile from context.
func (h *AuthHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	authUser, ok := AuthUserFromContext(r.Context())
	if !ok {
		respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
		return
	}

	respondJSON(w, http.StatusOK, authUser)
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

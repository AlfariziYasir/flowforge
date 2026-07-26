package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// AuthMiddleware provides HTTP middleware for JWT authentication and token revocation checks.
type AuthMiddleware struct {
	jwtService JWTService
	blacklist  TokenBlacklist
}

// NewAuthMiddleware creates a new AuthMiddleware instance with default noop blacklist.
func NewAuthMiddleware(jwtService JWTService) *AuthMiddleware {
	return NewAuthMiddlewareWithBlacklist(jwtService, NewNoopTokenBlacklist())
}

// NewAuthMiddlewareWithBlacklist creates an AuthMiddleware instance with custom TokenBlacklist.
func NewAuthMiddlewareWithBlacklist(jwtService JWTService, blacklist TokenBlacklist) *AuthMiddleware {
	if blacklist == nil {
		blacklist = NewNoopTokenBlacklist()
	}
	return &AuthMiddleware{
		jwtService: jwtService,
		blacklist:  blacklist,
	}
}

// Authenticate extracts the Bearer JWT token, checks revocation, and injects AuthUser into context.
func (m *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "missing authorization header")
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid authorization header format")
			return
		}

		tokenStr := parts[1]
		claims, err := m.jwtService.ValidateAccessToken(tokenStr)
		if err != nil {
			respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired token")
			return
		}

		if claims.JTI != "" {
			revoked, err := m.blacklist.IsRevoked(r.Context(), claims.JTI)
			if err == nil && revoked {
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "token has been revoked")
				return
			}
		}

		authUser := AuthUser{
			ID:       claims.UserID,
			TenantID: claims.TenantID,
			Email:    claims.Email,
			Role:     claims.Role,
		}

		ctx := ContextWithAuthUser(r.Context(), authUser)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole enforces role-based authorization for protected routes.
func RequireRole(allowedRoles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authUser, ok := AuthUserFromContext(r.Context())
			if !ok {
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "authentication required")
				return
			}

			allowed := false
			for _, role := range allowedRoles {
				if strings.EqualFold(authUser.Role, role) {
					allowed = true
					break
				}
			}

			if !allowed {
				respondJSONError(w, http.StatusForbidden, "Forbidden", "insufficient role permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func respondJSONError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   errType,
		Message: msg,
	})
}

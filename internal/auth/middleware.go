package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type AuthMiddleware struct {
	jwtService   JWTService
	blacklist    TokenBlacklist
	sessionStore SessionStore
}

func NewAuthMiddleware(jwtService JWTService) *AuthMiddleware {
	return NewAuthMiddlewareWithSessionStore(jwtService, NewNoopTokenBlacklist(), NewNoopSessionStore())
}

func NewAuthMiddlewareWithBlacklist(jwtService JWTService, blacklist TokenBlacklist) *AuthMiddleware {
	return NewAuthMiddlewareWithSessionStore(jwtService, blacklist, NewNoopSessionStore())
}

func NewAuthMiddlewareWithSessionStore(jwtService JWTService, blacklist TokenBlacklist, sessionStore SessionStore) *AuthMiddleware {
	if blacklist == nil {
		blacklist = NewNoopTokenBlacklist()
	}
	if sessionStore == nil {
		sessionStore = NewNoopSessionStore()
	}
	return &AuthMiddleware{
		jwtService:   jwtService,
		blacklist:    blacklist,
		sessionStore: sessionStore,
	}
}

// extractBearerToken reads the credential from either the Authorization header
// or, when absent, a "token" query parameter — needed because browsers' native
// EventSource API cannot set custom request headers, so SSE has no other way
// to authenticate. Every other route continues to use the header only; this
// fallback is consulted solely by routes configured with allowQueryParam=true.
func extractBearerToken(r *http.Request, allowQueryParam bool) (string, bool) {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1], true
		}
		return "", false
	}
	if allowQueryParam {
		if t := r.URL.Query().Get("token"); t != "" {
			return t, true
		}
	}
	return "", false
}

func (m *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return m.AuthenticateWithTokenSource(false)(next)
}

func (m *AuthMiddleware) AuthenticateWithTokenSource(allowQueryParam bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, ok := extractBearerToken(r, allowQueryParam)
			if !ok {
				if authHeader := r.Header.Get("Authorization"); authHeader != "" {
					respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid authorization header format")
					return
				}
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "missing authorization header")
				return
			}

			claims, err := m.jwtService.ValidateAccessToken(tokenStr)
			if err != nil {
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired token")
				return
			}

			if claims.JTI() != "" {
				revoked, err := m.blacklist.IsRevoked(r.Context(), claims.JTI())
				if err != nil {
					respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "unable to verify token revocation status")
					return
				}
				if revoked {
					respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "token has been revoked")
					return
				}
			}

			if claims.IssuedAt == nil {
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired token")
				return
			}

			revokedUser, err := m.sessionStore.IsUserRevoked(r.Context(), claims.UserID(), claims.IssuedAt.Time)
			if err != nil {
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "unable to verify user revocation status")
				return
			}
			if revokedUser {
				respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "user token has been revoked")
				return
			}

			if claims.SessionID != uuid.Nil {
				_, err := m.sessionStore.GetSession(r.Context(), claims.TenantID, claims.UserID(), claims.SessionID)
				if err != nil {
					if errors.Is(err, ErrSessionNotFound) {
						respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "session has expired or been revoked")
						return
					}
					respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "unable to verify session status")
					return
				}
			}

			authUser := AuthUser{
				ID:       claims.UserID(),
				TenantID: claims.TenantID,
				Email:    claims.Email,
				Role:     claims.Role,
			}

			ctx := ContextWithAuthUser(r.Context(), authUser)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

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

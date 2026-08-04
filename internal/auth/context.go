package auth

import (
	"context"

	"github.com/google/uuid"
)

type contextKey string

const authUserKey contextKey = "auth_user"

// AuthUser represents the authenticated user details extracted from JWT.
type AuthUser struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenantId"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
}

// ContextWithAuthUser returns a new context containing the AuthUser.
func ContextWithAuthUser(ctx context.Context, user AuthUser) context.Context {
	return context.WithValue(ctx, authUserKey, user)
}

// AuthUserFromContext retrieves the AuthUser from context.
func AuthUserFromContext(ctx context.Context) (AuthUser, bool) {
	user, ok := ctx.Value(authUserKey).(AuthUser)
	return user, ok
}

// TenantIDFromContext retrieves the TenantID from the AuthUser in context, or uuid.Nil if absent.
func TenantIDFromContext(ctx context.Context) uuid.UUID {
	if user, ok := AuthUserFromContext(ctx); ok {
		return user.TenantID
	}
	return uuid.Nil
}

package auth_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
)

func TestJWTService_GenerateTokenPair(t *testing.T) {
	secret := "test-secret-key-12345"
	svc := auth.NewJWTService(secret, 15*time.Minute, 7*24*time.Hour)

	t.Run("generates valid access and refresh tokens with session ID", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userID := uuid.New()
		tenantID := uuid.New()
		sessionID := uuid.New()
		email := "admin@flowforge.local"
		role := "admin"

		pair, err := svc.GenerateTokenPair(userID, tenantID, sessionID, email, role)
		req.NoError(err)
		is.NotEmpty(pair.AccessToken)
		is.NotEmpty(pair.RefreshToken)
		is.Greater(pair.ExpiresIn, int64(0))
	})
}

func TestJWTService_ValidateAccessToken(t *testing.T) {
	secret := "test-secret-key-12345"
	svc := auth.NewJWTService(secret, 15*time.Minute, 7*24*time.Hour)

	t.Run("validates correct access token and extracts claims", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userID := uuid.New()
		tenantID := uuid.New()
		sessionID := uuid.New()
		email := "editor@flowforge.local"
		role := "editor"

		pair, err := svc.GenerateTokenPair(userID, tenantID, sessionID, email, role)
		req.NoError(err)

		claims, err := svc.ValidateAccessToken(pair.AccessToken)
		req.NoError(err)
		is.Equal(userID, claims.UserID())
		is.Equal(tenantID, claims.TenantID)
		is.Equal(sessionID, claims.SessionID)
		is.Equal(email, claims.Email)
		is.Equal(role, claims.Role)
		is.Equal("access", claims.TokenType)
		is.NotEmpty(claims.JTI())
	})

	t.Run("rejects token signed with wrong secret", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userID := uuid.New()
		tenantID := uuid.New()
		sessionID := uuid.New()
		pair, err := svc.GenerateTokenPair(userID, tenantID, sessionID, "user@flowforge.local", "admin")
		req.NoError(err)

		wrongSvc := auth.NewJWTService("wrong-secret-key", 15*time.Minute, 7*24*time.Hour)
		_, err = wrongSvc.ValidateAccessToken(pair.AccessToken)
		is.Error(err)
	})

	t.Run("rejects expired token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		expiredSvc := auth.NewJWTService(secret, -1*time.Minute, 7*24*time.Hour)
		pair, err := expiredSvc.GenerateTokenPair(uuid.New(), uuid.New(), uuid.New(), "viewer@flowforge.local", "viewer")
		req.NoError(err)

		_, err = svc.ValidateAccessToken(pair.AccessToken)
		is.Error(err)
	})

	t.Run("rejects refresh token when expecting access token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		pair, err := svc.GenerateTokenPair(uuid.New(), uuid.New(), uuid.New(), "admin@flowforge.local", "admin")
		req.NoError(err)

		_, err = svc.ValidateAccessToken(pair.RefreshToken)
		is.Error(err)
	})
}

func TestJWTService_ValidateRefreshToken(t *testing.T) {
	secret := "test-secret-key-12345"
	svc := auth.NewJWTService(secret, 15*time.Minute, 7*24*time.Hour)

	t.Run("validates correct refresh token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userID := uuid.New()
		tenantID := uuid.New()
		sessionID := uuid.New()
		email := "admin@flowforge.local"

		pair, err := svc.GenerateTokenPair(userID, tenantID, sessionID, email, "admin")
		req.NoError(err)

		claims, err := svc.ValidateRefreshToken(pair.RefreshToken)
		req.NoError(err)
		is.Equal(userID, claims.UserID())
		is.Equal(tenantID, claims.TenantID)
		is.Equal(sessionID, claims.SessionID)
		is.Equal("refresh", claims.TokenType)
	})

	t.Run("rejects access token when expecting refresh token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		pair, err := svc.GenerateTokenPair(uuid.New(), uuid.New(), uuid.New(), "admin@flowforge.local", "admin")
		req.NoError(err)

		_, err = svc.ValidateRefreshToken(pair.AccessToken)
		is.Error(err)
	})
}

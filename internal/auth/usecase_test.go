package auth_test

import (
	"bytes"
	"context"
	"log/slog"
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

func TestAuthUseCase_Login(t *testing.T) {
	ctx := context.Background()

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

	inactiveUser := &domain.User{
		ID:           uuid.New(),
		TenantID:     tnt.ID,
		Email:        "disabled@flowforge.local",
		PasswordHash: hashedPass,
		Role:         "viewer",
		IsActive:     false,
	}

	t.Run("successfully authenticates with valid credentials", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		tenantUC.EXPECT().GetBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "admin@flowforge.local").Return(user, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().CreateSession(mock.Anything, mock.Anything, 7*24*time.Hour).Return(nil)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, nil, sessionStore, 7*24*time.Hour)

		res, err := uc.Login(ctx, "default-tenant", "admin@flowforge.local", "SecretP@ss123", "192.168.1.10", "Mozilla/5.0")
		req.NoError(err)
		is.NotEmpty(res.AccessToken)
		is.NotEmpty(res.RefreshToken)
		is.Equal(user.ID, res.User.ID)
	})

	t.Run("sanitizes email and slug casing", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		tenantUC.EXPECT().GetBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "admin@flowforge.local").Return(user, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().CreateSession(mock.Anything, mock.Anything, 7*24*time.Hour).Return(nil)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, nil, sessionStore, 7*24*time.Hour)

		res, err := uc.Login(ctx, " DEFAULT-TENANT ", " ADMIN@FLOWFORGE.LOCAL ", "SecretP@ss123", "127.0.0.1", "curl/7.68.0")
		req.NoError(err)
		is.NotEmpty(res.AccessToken)
	})

	t.Run("returns ErrUnauthorized on invalid password", func(t *testing.T) {
		is := assert.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		tenantUC.EXPECT().GetBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "admin@flowforge.local").Return(user, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, nil, nil, 7*24*time.Hour)

		_, err := uc.Login(ctx, "default-tenant", "admin@flowforge.local", "WrongPassword", "127.0.0.1", "test")
		is.ErrorIs(err, auth.ErrUnauthorized)
	})

	t.Run("returns ErrUnauthorized when tenant slug is unknown", func(t *testing.T) {
		is := assert.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		tenantUC.EXPECT().GetBySlug(mock.Anything, "unknown-tenant").Return(nil, tenant.ErrTenantNotFound)

		userRepo := authmocks.NewMockUserRepository(t)
		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, nil, nil, 7*24*time.Hour)

		_, err := uc.Login(ctx, "unknown-tenant", "admin@flowforge.local", "SecretP@ss123", "127.0.0.1", "test")
		is.ErrorIs(err, auth.ErrUnauthorized)
	})

	t.Run("returns ErrUserInactive when user account is inactive", func(t *testing.T) {
		is := assert.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		tenantUC.EXPECT().GetBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "disabled@flowforge.local").Return(inactiveUser, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, nil, nil, 7*24*time.Hour)

		_, err := uc.Login(ctx, "default-tenant", "disabled@flowforge.local", "SecretP@ss123", "127.0.0.1", "test")
		is.ErrorIs(err, auth.ErrUnauthorized)
	})

	t.Run("Login: logs the inactive-account reason", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		tenantUC.EXPECT().GetBySlug(mock.Anything, "default-tenant").Return(tnt, nil)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "disabled@flowforge.local").Return(inactiveUser, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)

		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		uc := auth.NewAuthUseCaseWithLogger(tenantUC, userRepo, jwtSvc, passSvc, nil, nil, 7*24*time.Hour, log)

		_, err := uc.Login(ctx, "default-tenant", "disabled@flowforge.local", "SecretP@ss123", "127.0.0.1", "test")
		is.ErrorIs(err, auth.ErrUnauthorized)
		req.Contains(buf.String(), "user account is inactive")
	})
}

func TestAuthUseCase_Refresh(t *testing.T) {
	ctx := context.Background()

	tnt := &domain.Tenant{ID: uuid.New(), Slug: "default-tenant"}
	passSvc := auth.NewPasswordServiceWithCost(bcrypt.MinCost)
	hashedPass, _ := passSvc.HashPassword("SecretP@ss123")
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tnt.ID,
		Email:        "user@flowforge.local",
		PasswordHash: hashedPass,
		Role:         "editor",
		IsActive:     true,
	}

	t.Run("refreshes token pair and revokes old refresh token", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tnt.ID, user.ID).Return(user, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().IsRevoked(mock.Anything, mock.AnythingOfType("string")).Return(false, nil)
		blacklist.EXPECT().Revoke(mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("time.Duration")).Return(nil)

		sessionID := uuid.New()
		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().IsUserRevoked(mock.Anything, user.ID, mock.Anything).Return(false, nil)
		sessionStore.EXPECT().GetSession(mock.Anything, tnt.ID, user.ID, sessionID).Return(&auth.UserSession{
			SessionID: sessionID,
			UserID:    user.ID,
			TenantID:  tnt.ID,
		}, nil)
		sessionStore.EXPECT().CreateSession(mock.Anything, mock.Anything, 7*24*time.Hour).Return(nil)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)

		pair, err := jwtSvc.GenerateTokenPair(user.ID, tnt.ID, sessionID, user.Email, user.Role)
		req.NoError(err)

		res, err := uc.Refresh(ctx, pair.RefreshToken)
		req.NoError(err)
		is.NotEmpty(res.AccessToken)
	})

	t.Run("rejects refresh when user has been revoked", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		userRepo := authmocks.NewMockUserRepository(t)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().IsRevoked(mock.Anything, mock.AnythingOfType("string")).Return(false, nil)

		sessionID := uuid.New()
		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().IsUserRevoked(mock.Anything, user.ID, mock.Anything).Return(true, nil)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)

		pair, err := jwtSvc.GenerateTokenPair(user.ID, tnt.ID, sessionID, user.Email, user.Role)
		req.NoError(err)

		_, err = uc.Refresh(ctx, pair.RefreshToken)
		is.ErrorIs(err, auth.ErrUnauthorized)
	})

	t.Run("Refresh: rotation TTL matches token lifetime", func(t *testing.T) {
		req := require.New(t)
		is := assert.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tnt.ID, user.ID).Return(user, nil)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)

		pair, err := jwtSvc.GenerateTokenPair(user.ID, tnt.ID, uuid.New(), user.Email, user.Role)
		req.NoError(err)

		claims, err := jwtSvc.ValidateRefreshToken(pair.RefreshToken)
		req.NoError(err)

		var capturedTTL time.Duration
		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().IsRevoked(mock.Anything, mock.AnythingOfType("string")).Return(false, nil)
		blacklist.EXPECT().Revoke(mock.Anything, claims.JTI(), mock.AnythingOfType("time.Duration")).RunAndReturn(func(ctx context.Context, jti string, ttl time.Duration) error {
			capturedTTL = ttl
			return nil
		})

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().IsUserRevoked(mock.Anything, user.ID, mock.Anything).Return(false, nil)
		sessionStore.EXPECT().GetSession(mock.Anything, tnt.ID, user.ID, claims.SessionID).Return(&auth.UserSession{
			SessionID: claims.SessionID,
			UserID:    user.ID,
			TenantID:  tnt.ID,
		}, nil)
		sessionStore.EXPECT().CreateSession(mock.Anything, mock.Anything, 7*24*time.Hour).Return(nil)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)

		_, err = uc.Refresh(ctx, pair.RefreshToken)
		req.NoError(err)

		is.InDelta((7 * 24 * time.Hour).Seconds(), capturedTTL.Seconds(), 5.0)
	})

	t.Run("Refresh: rejects a token with no session ID", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		pair, err := jwtSvc.GenerateTokenPair(user.ID, tnt.ID, uuid.Nil, user.Email, user.Role)
		req.NoError(err)

		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().IsRevoked(mock.Anything, mock.AnythingOfType("string")).Return(false, nil)

		uc := auth.NewAuthUseCase(nil, nil, jwtSvc, passSvc, blacklist, nil, 7*24*time.Hour)

		_, err = uc.Refresh(ctx, pair.RefreshToken)
		is.ErrorIs(err, auth.ErrUnauthorized)
	})

	t.Run("Refresh: revokes the old JTI before issuing new tokens", func(t *testing.T) {
		req := require.New(t)
		is := assert.New(t)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tnt.ID, user.ID).Return(user, nil)

		realJwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		pair, err := realJwtSvc.GenerateTokenPair(user.ID, tnt.ID, uuid.New(), user.Email, user.Role)
		req.NoError(err)

		claims, err := realJwtSvc.ValidateRefreshToken(pair.RefreshToken)
		req.NoError(err)

		var callOrder []string
		mockJwtSvc := authmocks.NewMockJWTService(t)
		mockJwtSvc.EXPECT().ValidateRefreshToken(pair.RefreshToken).Return(claims, nil)
		mockJwtSvc.EXPECT().GenerateTokenPair(user.ID, tnt.ID, claims.SessionID, user.Email, user.Role).RunAndReturn(func(userID, tenantID, sessionID uuid.UUID, email, role string) (*auth.TokenPair, error) {
			callOrder = append(callOrder, "GenerateTokenPair")
			return &auth.TokenPair{AccessToken: "newAccess", RefreshToken: "newRefresh", ExpiresIn: 900}, nil
		})

		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().IsRevoked(mock.Anything, mock.AnythingOfType("string")).Return(false, nil)
		blacklist.EXPECT().Revoke(mock.Anything, claims.JTI(), mock.AnythingOfType("time.Duration")).RunAndReturn(func(ctx context.Context, jti string, ttl time.Duration) error {
			callOrder = append(callOrder, "RevokeOldJTI")
			return nil
		})

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().IsUserRevoked(mock.Anything, user.ID, mock.Anything).Return(false, nil)
		sessionStore.EXPECT().GetSession(mock.Anything, tnt.ID, user.ID, claims.SessionID).Return(&auth.UserSession{
			SessionID: claims.SessionID,
			UserID:    user.ID,
			TenantID:  tnt.ID,
		}, nil)
		sessionStore.EXPECT().CreateSession(mock.Anything, mock.Anything, 7*24*time.Hour).RunAndReturn(func(ctx context.Context, session *auth.UserSession, ttl time.Duration) error {
			callOrder = append(callOrder, "CreateNewSession")
			return nil
		})

		uc := auth.NewAuthUseCase(tenantUC, userRepo, mockJwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)

		_, err = uc.Refresh(ctx, pair.RefreshToken)
		req.NoError(err)

		require.Len(t, callOrder, 3)
		is.Equal("RevokeOldJTI", callOrder[0], "expected old JTI to be revoked BEFORE creating new session and generating tokens")
		is.Equal("CreateNewSession", callOrder[1])
		is.Equal("GenerateTokenPair", callOrder[2])
	})
}

func TestAuthUseCase_Logout(t *testing.T) {
	ctx := context.Background()

	t.Run("revokes valid bearer access token JTI and session", func(t *testing.T) {
		req := require.New(t)

		userID := uuid.New()
		tenantID := uuid.New()
		sessionID := uuid.New()
		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)

		pair, err := jwtSvc.GenerateTokenPair(userID, tenantID, sessionID, "user@flowforge.local", "admin")
		req.NoError(err)

		claims, err := jwtSvc.ValidateAccessToken(pair.AccessToken)
		req.NoError(err)

		tenantUC := tenantmocks.NewMockTenantUseCase(t)
		userRepo := authmocks.NewMockUserRepository(t)
		passSvc := auth.NewPasswordServiceWithCost(bcrypt.MinCost)
		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().Revoke(mock.Anything, claims.JTI(), mock.AnythingOfType("time.Duration")).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().RevokeSession(mock.Anything, tenantID, userID, sessionID).Return(nil)

		uc := auth.NewAuthUseCase(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, 7*24*time.Hour)

		err = uc.Logout(ctx, pair.AccessToken)
		req.NoError(err)
	})

	t.Run("returns ErrUnauthorized for malformed or unparseable token string", func(t *testing.T) {
		is := assert.New(t)
		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		uc := auth.NewAuthUseCase(nil, nil, jwtSvc, nil, nil, nil, 7*24*time.Hour)

		err := uc.Logout(ctx, "invalid.garbage.token")
		is.ErrorIs(err, auth.ErrUnauthorized)
	})

	t.Run("Logout: blacklist TTL covers the token's remaining lifetime", func(t *testing.T) {
		req := require.New(t)
		is := assert.New(t)

		tenantID := uuid.New()
		userID := uuid.New()
		sessionID := uuid.New()

		jwtSvc := auth.NewJWTService("auth-uc-test-secret-key-32chars!!", 15*time.Minute, 7*24*time.Hour)
		pair, err := jwtSvc.GenerateTokenPair(userID, tenantID, sessionID, "user@flowforge.local", "editor")
		req.NoError(err)

		claims, err := jwtSvc.ValidateAccessToken(pair.AccessToken)
		req.NoError(err)

		var capturedTTL time.Duration
		blacklist := authmocks.NewMockTokenBlacklist(t)
		blacklist.EXPECT().Revoke(mock.Anything, claims.JTI(), mock.AnythingOfType("time.Duration")).RunAndReturn(func(ctx context.Context, jti string, ttl time.Duration) error {
			capturedTTL = ttl
			return nil
		})

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().RevokeSession(mock.Anything, tenantID, userID, sessionID).Return(nil)

		uc := auth.NewAuthUseCase(nil, nil, jwtSvc, auth.NewPasswordServiceWithCost(bcrypt.MinCost), blacklist, sessionStore, 7*24*time.Hour)
		err = uc.Logout(ctx, pair.AccessToken)
		req.NoError(err)

		is.InDelta(15*time.Minute.Seconds(), capturedTTL.Seconds(), 5.0)
	})
}

func TestAuthUseCase_LogoutAllDevices(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully revokes user timestamp and all sessions", func(t *testing.T) {
		req := require.New(t)

		tenantID := uuid.New()
		userID := uuid.New()

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, userID, mock.Anything, 7*24*time.Hour).Return(nil)
		sessionStore.EXPECT().RevokeAllUserSessions(mock.Anything, tenantID, userID).Return(nil)

		uc := auth.NewAuthUseCase(nil, nil, nil, nil, nil, sessionStore, 7*24*time.Hour)

		err := uc.LogoutAllDevices(ctx, tenantID, userID)
		req.NoError(err)
	})
}

func TestAuthUseCase_ListSessions(t *testing.T) {
	ctx := context.Background()

	t.Run("returns paginated user sessions", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		tenantID := uuid.New()
		userID := uuid.New()

		mockSessions := []*auth.UserSession{
			{SessionID: uuid.New(), UserID: userID, TenantID: tenantID},
			{SessionID: uuid.New(), UserID: userID, TenantID: tenantID},
		}

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().ListUserSessions(mock.Anything, tenantID, userID, 1, 10).Return(mockSessions, int64(2), nil)

		uc := auth.NewAuthUseCase(nil, nil, nil, nil, nil, sessionStore, 7*24*time.Hour)

		res, err := uc.ListSessions(ctx, tenantID, userID, 1, 10)
		req.NoError(err)
		is.Equal(int64(2), res.Total)
		is.Len(res.Sessions, 2)
		is.Equal(1, res.Page)
		is.Equal(10, res.Size)
	})

	t.Run("returns ErrUnauthorized on zero tenant or user ID", func(t *testing.T) {
		is := assert.New(t)

		uc := auth.NewAuthUseCase(nil, nil, nil, nil, nil, nil, 7*24*time.Hour)

		_, err := uc.ListSessions(ctx, uuid.Nil, uuid.New(), 1, 10)
		is.ErrorIs(err, auth.ErrUnauthorized)
	})
}

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/tenant"
)

var ErrUnauthorized = errors.New("unauthorized")

var getDummyBcryptHash = sync.OnceValue(func() string {
	passSvc := NewPasswordService()
	h, err := passSvc.HashPassword("flowforge-dummy-password-never-used")
	if err != nil {
		return "$2a$12$e8869s27T.p.oV.o/.10.u8V/1G0hP5K.uJ4e7yK8yK8yK8yK8yK8"
	}
	return h
})

type AuthResult struct {
	AccessToken  string       `json:"accessToken"`
	RefreshToken string       `json:"refreshToken"`
	ExpiresIn    int64        `json:"expiresIn"`
	User         *domain.User `json:"user"`
}

type AuthUseCase interface {
	Login(ctx context.Context, tenantSlug, email, password string, ipAddress, userAgent string) (*AuthResult, error)
	Refresh(ctx context.Context, refreshTokenStr string) (*AuthResult, error)
	Logout(ctx context.Context, tokenStr string) error
	LogoutAllDevices(ctx context.Context, tenantID, userID uuid.UUID) error
	ListSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) (*PaginatedSessions, error)
	GetMe(ctx context.Context, tenantID, userID uuid.UUID) (*domain.User, error)
}

type PaginatedSessions struct {
	Sessions []*UserSession `json:"sessions"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	Size     int            `json:"pageSize"`
}

type authUseCase struct {
	tenantUC      tenant.TenantUseCase
	userRepo      UserRepository
	jwtSvc        JWTService
	passSvc       PasswordService
	blacklist     TokenBlacklist
	sessionStore  SessionStore
	refreshExpiry time.Duration
	audit         domain.AuditRepository
	logger        *slog.Logger
	dummyHash     string
}

func NewAuthUseCase(
	tenantUC tenant.TenantUseCase,
	userRepo UserRepository,
	jwtSvc JWTService,
	passSvc PasswordService,
	blacklist TokenBlacklist,
	sessionStore SessionStore,
	refreshExpiry time.Duration,
) AuthUseCase {
	return NewAuthUseCaseWithAudit(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, refreshExpiry, nil, nil)
}

func NewAuthUseCaseWithLogger(
	tenantUC tenant.TenantUseCase,
	userRepo UserRepository,
	jwtSvc JWTService,
	passSvc PasswordService,
	blacklist TokenBlacklist,
	sessionStore SessionStore,
	refreshExpiry time.Duration,
	logger *slog.Logger,
) AuthUseCase {
	return NewAuthUseCaseWithAudit(tenantUC, userRepo, jwtSvc, passSvc, blacklist, sessionStore, refreshExpiry, nil, logger)
}

func NewAuthUseCaseWithAudit(
	tenantUC tenant.TenantUseCase,
	userRepo UserRepository,
	jwtSvc JWTService,
	passSvc PasswordService,
	blacklist TokenBlacklist,
	sessionStore SessionStore,
	refreshExpiry time.Duration,
	audit domain.AuditRepository,
	logger *slog.Logger,
) AuthUseCase {
	if refreshExpiry <= 0 {
		refreshExpiry = 7 * 24 * time.Hour
	}
	if blacklist == nil {
		blacklist = NewNoopTokenBlacklist()
	}
	if sessionStore == nil {
		sessionStore = NewNoopSessionStore()
	}
	if logger == nil {
		logger = slog.Default()
	}

	// NOTE: dummyHash is generated using passSvc on initialization to preserve timing equivalence
	// between real and dummy password comparisons while allowing unit tests to use MinCost for fast execution.
	var dummyHash string
	if passSvc != nil {
		if h, err := passSvc.HashPassword("flowforge-dummy-password-never-used"); err == nil {
			dummyHash = h
		}
	}
	if dummyHash == "" {
		dummyHash = getDummyBcryptHash()
	}

	return &authUseCase{
		tenantUC:      tenantUC,
		userRepo:      userRepo,
		jwtSvc:        jwtSvc,
		passSvc:       passSvc,
		blacklist:     blacklist,
		sessionStore:  sessionStore,
		refreshExpiry: refreshExpiry,
		audit:         audit,
		logger:        logger,
		dummyHash:     dummyHash,
	}
}

func (u *authUseCase) revokeTokenUntilExpiry(ctx context.Context, claims *CustomClaims) error {
	if claims == nil || claims.JTI() == "" || claims.ExpiresAt == nil {
		return nil
	}
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl > 0 {
		if err := u.blacklist.Revoke(ctx, claims.JTI(), ttl); err != nil {
			return fmt.Errorf("revoke token in blacklist: %w", err)
		}
	}
	return nil
}

func (u *authUseCase) Login(ctx context.Context, tenantSlug, email, password string, ipAddress, userAgent string) (*AuthResult, error) {
	sanitizedSlug := strings.ToLower(strings.TrimSpace(tenantSlug))
	sanitizedEmail := strings.ToLower(strings.TrimSpace(email))

	if sanitizedSlug == "" || sanitizedEmail == "" || password == "" {
		return nil, ErrUnauthorized
	}

	tnt, err := u.tenantUC.GetBySlug(ctx, sanitizedSlug)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			_ = u.passSvc.ComparePassword(u.dummyHash, password)
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("lookup tenant by slug: %w", err)
	}

	usr, err := u.userRepo.FindByEmail(ctx, tnt.ID, sanitizedEmail)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			_ = u.passSvc.ComparePassword(u.dummyHash, password)
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("lookup user by email: %w", err)
	}

	if !usr.IsActive {
		_ = u.passSvc.ComparePassword(u.dummyHash, password)
		u.logger.Info("login attempt rejected: user account is inactive", slog.String("tenantSlug", sanitizedSlug), slog.String("email", sanitizedEmail))
		return nil, ErrUnauthorized
	}

	if err := u.passSvc.ComparePassword(usr.PasswordHash, password); err != nil {
		return nil, ErrUnauthorized
	}

	sessionID := uuid.New()
	now := time.Now()
	sess := &UserSession{
		SessionID:    sessionID,
		UserID:       usr.ID,
		TenantID:     usr.TenantID,
		IPAddress:    ipAddress,
		UserAgent:    userAgent,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    now.Add(u.refreshExpiry),
	}

	if err := u.sessionStore.CreateSession(ctx, sess, u.refreshExpiry); err != nil {
		return nil, fmt.Errorf("create user session: %w", err)
	}

	pair, err := u.jwtSvc.GenerateTokenPair(usr.ID, usr.TenantID, sessionID, usr.Email, usr.Role)
	if err != nil {
		return nil, fmt.Errorf("generate auth tokens: %w", err)
	}

	if u.audit != nil {
		meta, _ := json.Marshal(map[string]string{
			"ipAddress": ipAddress,
			"userAgent": userAgent,
		})
		_ = u.audit.Record(ctx, domain.AuditEntry{
			TenantID:    usr.TenantID,
			ActorUserID: &usr.ID,
			Action:      ActionUserLoggedIn,
			EntityType:  "user",
			EntityID:    &usr.ID,
			Metadata:    meta,
		})
	}

	return &AuthResult{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		User:         usr,
	}, nil
}

func (u *authUseCase) Refresh(ctx context.Context, refreshTokenStr string) (*AuthResult, error) {
	if refreshTokenStr == "" {
		return nil, ErrUnauthorized
	}

	claims, err := u.jwtSvc.ValidateRefreshToken(refreshTokenStr)
	if err != nil {
		return nil, ErrUnauthorized
	}

	if claims.IssuedAt == nil {
		return nil, ErrUnauthorized
	}

	if claims.JTI() != "" {
		revoked, err := u.blacklist.IsRevoked(ctx, claims.JTI())
		if err != nil {
			return nil, fmt.Errorf("check token revocation status: %w", err)
		}
		if revoked {
			return nil, ErrUnauthorized
		}
	}

	revokedUser, err := u.sessionStore.IsUserRevoked(ctx, claims.UserID(), claims.IssuedAt.Time)
	if err != nil {
		return nil, fmt.Errorf("check user revocation status: %w", err)
	}
	if revokedUser {
		return nil, ErrUnauthorized
	}

	sessionID := claims.SessionID
	if sessionID == uuid.Nil {
		return nil, ErrUnauthorized
	}

	sess, err := u.sessionStore.GetSession(ctx, claims.TenantID, claims.UserID(), sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("verify session status on refresh: %w", err)
	}

	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}

	maxSessionLifetime := 30 * 24 * time.Hour
	if now.Sub(sess.CreatedAt) > maxSessionLifetime {
		_ = u.sessionStore.RevokeSession(ctx, claims.TenantID, claims.UserID(), sessionID)
		return nil, ErrUnauthorized
	}

	usr, err := u.userRepo.FindByID(ctx, claims.TenantID, claims.UserID())
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("lookup user by id: %w", err)
	}

	if !usr.IsActive {
		return nil, ErrUserInactive
	}

	// INVARIANT (B-9): Revocation of the old refresh token JTI must precede session update
	// and new token issuance. Reordering these operations reopens the concurrent-double-refresh race window.
	if err := u.revokeTokenUntilExpiry(ctx, claims); err != nil {
		return nil, fmt.Errorf("revoke rotated refresh token: %w", err)
	}

	sess.LastActiveAt = now
	sess.ExpiresAt = now.Add(u.refreshExpiry)
	if err := u.sessionStore.CreateSession(ctx, sess, u.refreshExpiry); err != nil {
		return nil, fmt.Errorf("update user session on refresh: %w", err)
	}

	pair, err := u.jwtSvc.GenerateTokenPair(usr.ID, usr.TenantID, sessionID, usr.Email, usr.Role)
	if err != nil {
		return nil, fmt.Errorf("generate tokens on refresh: %w", err)
	}

	return &AuthResult{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		User:         usr,
	}, nil
}

func (u *authUseCase) Logout(ctx context.Context, tokenStr string) error {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return ErrUnauthorized
	}

	claims, err := u.jwtSvc.ValidateAccessToken(tokenStr)
	if err != nil {
		claims, err = u.jwtSvc.ValidateRefreshToken(tokenStr)
	}

	if err != nil {
		return ErrUnauthorized
	}

	if claims.ExpiresAt == nil {
		u.logger.Warn("logout token missing expiresAt claim", slog.String("subject", claims.Subject))
		if claims.SessionID != uuid.Nil {
			_ = u.sessionStore.RevokeSession(ctx, claims.TenantID, claims.UserID(), claims.SessionID)
		}
		return ErrUnauthorized
	}

	if err := u.revokeTokenUntilExpiry(ctx, claims); err != nil {
		return fmt.Errorf("revoke token on logout: %w", err)
	}

	if claims.SessionID != uuid.Nil {
		if err := u.sessionStore.RevokeSession(ctx, claims.TenantID, claims.UserID(), claims.SessionID); err != nil {
			return fmt.Errorf("revoke session on logout: %w", err)
		}
	}

	return nil
}

func (u *authUseCase) LogoutAllDevices(ctx context.Context, tenantID, userID uuid.UUID) error {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return ErrUnauthorized
	}

	if err := u.sessionStore.SetUserRevokedBefore(ctx, userID, time.Now(), u.refreshExpiry); err != nil {
		return fmt.Errorf("set user revoked before timestamp: %w", err)
	}

	if err := u.sessionStore.RevokeAllUserSessions(ctx, tenantID, userID); err != nil {
		return fmt.Errorf("revoke all user sessions: %w", err)
	}

	return nil
}

func (u *authUseCase) ListSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) (*PaginatedSessions, error) {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	sessions, total, err := u.sessionStore.ListUserSessions(ctx, tenantID, userID, page, pageSize)
	if err != nil {
		return nil, fmt.Errorf("list user sessions: %w", err)
	}

	if sessions == nil {
		sessions = []*UserSession{}
	}

	return &PaginatedSessions{
		Sessions: sessions,
		Total:    total,
		Page:     page,
		Size:     pageSize,
	}, nil
}

func (u *authUseCase) GetMe(ctx context.Context, tenantID, userID uuid.UUID) (*domain.User, error) {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	return u.userRepo.FindByID(ctx, tenantID, userID)
}

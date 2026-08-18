package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
)

var (
	ErrCannotDeactivateSelf = errors.New("cannot deactivate own user account")
	ErrCannotDeleteSelf     = errors.New("cannot delete own user account")
)

type CreateUserCommand struct {
	ActorID  uuid.UUID `json:"-"`
	TenantID uuid.UUID `json:"tenantId"`
	Email    string    `json:"email"`
	Password string    `json:"password"`
	Role     string    `json:"role"`
}

type UpdateUserCommand struct {
	ActorID  uuid.UUID `json:"-"`
	TenantID uuid.UUID `json:"tenantId"`
	UserID   uuid.UUID `json:"userId"`
	Email    *string   `json:"email,omitempty"`
	Role     *string   `json:"role,omitempty"`
	IsActive *bool     `json:"isActive,omitempty"`
}

type ListUsersQuery struct {
	TenantID   uuid.UUID
	Page       int
	PageSize   int
	OrderBy    string
	Role       string
	IsAsc      *bool
	ActiveOnly *bool
}

type PaginatedUsers struct {
	Users []*domain.User `json:"users"`
	Total int64          `json:"total"`
	Page  int            `json:"page"`
	Size  int            `json:"pageSize"`
}

type UserUseCase interface {
	CreateUser(ctx context.Context, cmd CreateUserCommand) (*domain.User, error)
	ListUsers(ctx context.Context, query ListUsersQuery) (*PaginatedUsers, error)
	GetUser(ctx context.Context, tenantID, userID uuid.UUID) (*domain.User, error)
	UpdateUser(ctx context.Context, cmd UpdateUserCommand) (*domain.User, error)
	DeleteUser(ctx context.Context, actorID, tenantID, targetID uuid.UUID) error
}

type TxRunner interface {
	ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type passthroughTxRunner struct{}

func (p *passthroughTxRunner) ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// NewPassthroughTxRunner returns a TxRunner that executes fn with no transaction boundary.
// Production callers owning a database pool must pass postgres.UnitOfWork instead — see cmd/api/main.go.
func NewPassthroughTxRunner() TxRunner {
	return &passthroughTxRunner{}
}

type userUseCase struct {
	userRepo      UserRepository
	passSvc       PasswordService
	sessionStore  SessionStore
	refreshExpiry time.Duration
	txRunner      TxRunner
	audit         domain.AuditRepository
}

// NewUserUseCase builds a default UserUseCase with passthrough transaction runner.
func NewUserUseCase(userRepo UserRepository, passSvc PasswordService, sessionStore SessionStore, refreshExpiry time.Duration) UserUseCase {
	return NewUserUseCaseWithTxAndAudit(userRepo, passSvc, sessionStore, refreshExpiry, NewPassthroughTxRunner(), nil)
}

// NewUserUseCaseWithTx builds a UserUseCase bound to a transaction boundary.
// txRunner must not be nil — pass NewPassthroughTxRunner() to opt out explicitly.
func NewUserUseCaseWithTx(userRepo UserRepository, passSvc PasswordService, sessionStore SessionStore, refreshExpiry time.Duration, txRunner TxRunner) UserUseCase {
	return NewUserUseCaseWithTxAndAudit(userRepo, passSvc, sessionStore, refreshExpiry, txRunner, nil)
}

// NewUserUseCaseWithTxAndAudit builds a UserUseCase with transaction and audit logging capabilities.
func NewUserUseCaseWithTxAndAudit(userRepo UserRepository, passSvc PasswordService, sessionStore SessionStore, refreshExpiry time.Duration, txRunner TxRunner, audit domain.AuditRepository) UserUseCase {
	if refreshExpiry <= 0 {
		refreshExpiry = 7 * 24 * time.Hour
	}
	if sessionStore == nil {
		sessionStore = NewNoopSessionStore()
	}
	if txRunner == nil {
		txRunner = NewPassthroughTxRunner()
	}
	return &userUseCase{
		userRepo:      userRepo,
		passSvc:       passSvc,
		sessionStore:  sessionStore,
		refreshExpiry: refreshExpiry,
		txRunner:      txRunner,
		audit:         audit,
	}
}

func (u *userUseCase) revokeUserAccess(ctx context.Context, tenantID, userID uuid.UUID) error {
	if err := u.sessionStore.SetUserRevokedBefore(ctx, userID, time.Now(), u.refreshExpiry); err != nil {
		return fmt.Errorf("set user revoked before timestamp: %w", err)
	}
	if err := u.sessionStore.RevokeAllUserSessions(ctx, tenantID, userID); err != nil {
		return fmt.Errorf("revoke all user sessions: %w", err)
	}
	return nil
}

func (u *userUseCase) CreateUser(ctx context.Context, cmd CreateUserCommand) (*domain.User, error) {
	if cmd.TenantID == uuid.Nil {
		return nil, errors.New("tenant ID is required")
	}

	sanitizedEmail := strings.ToLower(strings.TrimSpace(cmd.Email))
	if sanitizedEmail == "" {
		return nil, errors.New("email is required")
	}

	role := strings.ToLower(strings.TrimSpace(cmd.Role))
	if role != "admin" && role != "editor" && role != "viewer" {
		return nil, ErrInvalidRole
	}

	hashedPass, err := u.passSvc.HashPassword(cmd.Password)
	if err != nil {
		return nil, err
	}

	usr := &domain.User{
		ID:           uuid.New(),
		TenantID:     cmd.TenantID,
		Email:        sanitizedEmail,
		PasswordHash: hashedPass,
		Role:         role,
		IsActive:     true,
	}

	err = u.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		if err := u.userRepo.CreateUser(txCtx, usr); err != nil {
			return err
		}
		if u.audit != nil {
			actorID := cmd.ActorID
			if actorID == uuid.Nil {
				actorID = usr.ID
			}
			meta, _ := json.Marshal(map[string]string{
				"email": usr.Email,
				"role":  usr.Role,
			})
			if err := u.audit.Record(txCtx, domain.AuditEntry{
				TenantID:    usr.TenantID,
				ActorUserID: &actorID,
				Action:      ActionUserCreated,
				EntityType:  "user",
				EntityID:    &usr.ID,
				Metadata:    meta,
			}); err != nil {
				return fmt.Errorf("audit user creation: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return usr, nil
}

func (u *userUseCase) ListUsers(ctx context.Context, query ListUsersQuery) (*PaginatedUsers, error) {
	if query.TenantID == uuid.Nil {
		return nil, errors.New("tenant ID is required")
	}

	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		query.PageSize = 20
	}

	users, total, err := u.userRepo.ListUsers(ctx, query.TenantID, query.Page, query.PageSize, query.OrderBy, query.Role, query.IsAsc, query.ActiveOnly)
	if err != nil {
		return nil, fmt.Errorf("list users from repo: %w", err)
	}

	if users == nil {
		users = []*domain.User{}
	}

	return &PaginatedUsers{
		Users: users,
		Total: total,
		Page:  query.Page,
		Size:  query.PageSize,
	}, nil
}

func (u *userUseCase) GetUser(ctx context.Context, tenantID, userID uuid.UUID) (*domain.User, error) {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return nil, ErrUserNotFound
	}
	return u.userRepo.FindByID(ctx, tenantID, userID)
}

func (u *userUseCase) UpdateUser(ctx context.Context, cmd UpdateUserCommand) (*domain.User, error) {
	if cmd.TenantID == uuid.Nil || cmd.UserID == uuid.Nil {
		return nil, ErrUserNotFound
	}

	usr, err := u.userRepo.FindByID(ctx, cmd.TenantID, cmd.UserID)
	if err != nil {
		return nil, err
	}

	if cmd.Email != nil {
		sanitizedEmail := strings.ToLower(strings.TrimSpace(*cmd.Email))
		if sanitizedEmail != "" {
			usr.Email = sanitizedEmail
		}
	}

	roleChanged := false
	if cmd.Role != nil {
		role := strings.ToLower(strings.TrimSpace(*cmd.Role))
		if role != "admin" && role != "editor" && role != "viewer" {
			return nil, ErrInvalidRole
		}
		if usr.Role != role {
			roleChanged = true
			usr.Role = role
		}
	}

	deactivating := false
	if cmd.IsActive != nil {
		if !*cmd.IsActive && cmd.ActorID == cmd.UserID {
			return nil, ErrCannotDeactivateSelf
		}
		if usr.IsActive && !*cmd.IsActive {
			deactivating = true
		}
		usr.IsActive = *cmd.IsActive
	}

	// NOTE: revokeUserAccess performs Redis operations inside the Postgres transaction.
	// If Redis fails, the DB update is rolled back (fail-safe). If Redis succeeds but DB commit fails,
	// sessions are revoked while DB state remains unchanged (safer partial failure mode).
	err = u.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		if err := u.userRepo.UpdateUser(txCtx, usr); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		if deactivating || roleChanged {
			if err := u.revokeUserAccess(txCtx, usr.TenantID, usr.ID); err != nil {
				return fmt.Errorf("revoke user access on update: %w", err)
			}
		}
		if u.audit != nil {
			meta, _ := json.Marshal(map[string]any{
				"email":    usr.Email,
				"role":     usr.Role,
				"isActive": usr.IsActive,
			})
			actorID := cmd.ActorID
			if actorID == uuid.Nil {
				actorID = usr.ID
			}
			if err := u.audit.Record(txCtx, domain.AuditEntry{
				TenantID:    usr.TenantID,
				ActorUserID: &actorID,
				Action:      ActionUserUpdated,
				EntityType:  "user",
				EntityID:    &usr.ID,
				Metadata:    meta,
			}); err != nil {
				return fmt.Errorf("audit user update: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return usr, nil
}

func (u *userUseCase) DeleteUser(ctx context.Context, actorID, tenantID, targetID uuid.UUID) error {
	if tenantID == uuid.Nil || targetID == uuid.Nil {
		return ErrUserNotFound
	}

	if actorID == targetID {
		return ErrCannotDeleteSelf
	}

	usr, err := u.userRepo.FindByID(ctx, tenantID, targetID)
	if err != nil {
		return err
	}

	usr.IsActive = false
	return u.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		if err := u.userRepo.UpdateUser(txCtx, usr); err != nil {
			return fmt.Errorf("deactivate user on delete: %w", err)
		}
		if err := u.revokeUserAccess(txCtx, usr.TenantID, usr.ID); err != nil {
			return fmt.Errorf("revoke user access on delete: %w", err)
		}
		return nil
	})
}

package auth_test

import (
	"context"
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
	domainmocks "flowforge/internal/domain/mocks"
)

type recordingTxRunner struct {
	entered  bool
	innerErr error
}

func (r *recordingTxRunner) ExecuteInTx(ctx context.Context, fn func(context.Context) error) error {
	r.entered = true
	r.innerErr = fn(ctx)
	return r.innerErr
}

func TestUserUseCase_CreateUser(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	passSvc := auth.NewPasswordServiceWithCost(bcrypt.MinCost)

	t.Run("successfully creates user with valid request", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().CreateUser(mock.Anything, mock.MatchedBy(func(u *domain.User) bool {
			return u.TenantID == tenantID && u.Email == "newuser@flowforge.local" && u.Role == "editor"
		})).Return(nil)

		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, nil, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		cmd := auth.CreateUserCommand{
			TenantID: tenantID,
			Email:    "  NewUser@FlowForge.Local  ",
			Password: "ValidPassword123",
			Role:     "editor",
		}

		user, err := uc.CreateUser(ctx, cmd)
		req.NoError(err)
		is.NotEqual(uuid.Nil, user.ID)
		is.Equal("newuser@flowforge.local", user.Email)
		is.Equal("editor", user.Role)
		is.True(user.IsActive)
	})

	t.Run("successfully audits user creation inside transaction", func(t *testing.T) {
		req := require.New(t)
		is := assert.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().CreateUser(mock.Anything, mock.Anything).Return(nil)

		auditRepo := domainmocks.NewMockAuditRepository(t)
		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e domain.AuditEntry) bool {
			return e.TenantID == tenantID && e.Action == auth.ActionUserCreated && e.EntityType == "user" && e.EntityID != nil
		})).Return(nil).Once()

		txRunner := &recordingTxRunner{}
		uc := auth.NewUserUseCaseWithTxAndAudit(userRepo, passSvc, nil, 7*24*time.Hour, txRunner, auditRepo)

		cmd := auth.CreateUserCommand{
			TenantID: tenantID,
			Email:    "auditeduser@flowforge.local",
			Password: "ValidPassword123",
			Role:     "editor",
		}

		user, err := uc.CreateUser(ctx, cmd)
		req.NoError(err)
		is.NotNil(user)
		is.True(txRunner.entered, "CreateUser must execute inside a transaction")
	})

	t.Run("successfully audits user creation with actual actor attribution", func(t *testing.T) {
		req := require.New(t)
		is := assert.New(t)

		actorID := uuid.New()
		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().CreateUser(mock.Anything, mock.Anything).Return(nil)

		auditRepo := domainmocks.NewMockAuditRepository(t)
		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e domain.AuditEntry) bool {
			return e.TenantID == tenantID &&
				e.Action == auth.ActionUserCreated &&
				e.EntityType == "user" &&
				e.EntityID != nil &&
				e.ActorUserID != nil &&
				*e.ActorUserID == actorID
		})).Return(nil).Once()

		txRunner := &recordingTxRunner{}
		uc := auth.NewUserUseCaseWithTxAndAudit(userRepo, passSvc, nil, 7*24*time.Hour, txRunner, auditRepo)

		cmd := auth.CreateUserCommand{
			ActorID:  actorID,
			TenantID: tenantID,
			Email:    "auditedactor@flowforge.local",
			Password: "ValidPassword123",
			Role:     "editor",
		}

		user, err := uc.CreateUser(ctx, cmd)
		req.NoError(err)
		is.NotNil(user)
		is.NotEqual(actorID, user.ID)
		is.True(txRunner.entered, "CreateUser must execute inside a transaction")
	})

	t.Run("returns error when password is too short", func(t *testing.T) {
		is := assert.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, nil, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		cmd := auth.CreateUserCommand{
			TenantID: tenantID,
			Email:    "user@flowforge.local",
			Password: "short",
			Role:     "viewer",
		}

		_, err := uc.CreateUser(ctx, cmd)
		is.ErrorIs(err, auth.ErrInvalidPasswordLength)
	})

	t.Run("returns error when role is invalid", func(t *testing.T) {
		is := assert.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, nil, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		cmd := auth.CreateUserCommand{
			TenantID: tenantID,
			Email:    "user2@flowforge.local",
			Password: "ValidPassword123",
			Role:     "superadmin",
		}

		_, err := uc.CreateUser(ctx, cmd)
		is.ErrorIs(err, auth.ErrInvalidRole)
	})
}

func TestUserUseCase_SelfDeactivationGuardAndRevocation(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	passSvc := auth.NewPasswordServiceWithCost(bcrypt.MinCost)

	hashedPass, _ := passSvc.HashPassword("SecretP@ss123")
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Email:        "admin@flowforge.local",
		PasswordHash: hashedPass,
		Role:         "admin",
		IsActive:     true,
	}

	t.Run("prevents admin from deactivating their own account", func(t *testing.T) {
		is := assert.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, user.ID).Return(user, nil)

		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, nil, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		activeState := false
		cmd := auth.UpdateUserCommand{
			ActorID:  user.ID,
			TenantID: tenantID,
			UserID:   user.ID,
			IsActive: &activeState,
		}

		_, err := uc.UpdateUser(ctx, cmd)
		is.ErrorIs(err, auth.ErrCannotDeactivateSelf)
	})

	t.Run("prevents admin from deleting their own account", func(t *testing.T) {
		is := assert.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, nil, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		err := uc.DeleteUser(ctx, user.ID, tenantID, user.ID)
		is.ErrorIs(err, auth.ErrCannotDeleteSelf)
	})

	t.Run("triggers revocation timestamp and session deletion when user is deactivated", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		activeTarget := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target1@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "editor",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, activeTarget.ID).Return(activeTarget, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.MatchedBy(func(u *domain.User) bool {
			return u.ID == activeTarget.ID && !u.IsActive
		})).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, activeTarget.ID, mock.Anything, 7*24*time.Hour).Return(nil)
		sessionStore.EXPECT().RevokeAllUserSessions(mock.Anything, tenantID, activeTarget.ID).Return(nil)

		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		activeState := false
		cmd := auth.UpdateUserCommand{
			ActorID:  user.ID,
			TenantID: tenantID,
			UserID:   activeTarget.ID,
			IsActive: &activeState,
		}

		updated, err := uc.UpdateUser(ctx, cmd)
		req.NoError(err)
		is.False(updated.IsActive)
	})

	t.Run("successfully audits user update inside transaction", func(t *testing.T) {
		req := require.New(t)
		is := assert.New(t)

		targetUser := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "viewer",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, targetUser.ID).Return(targetUser, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.Anything).Return(nil)

		auditRepo := domainmocks.NewMockAuditRepository(t)
		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e domain.AuditEntry) bool {
			return e.TenantID == tenantID && e.ActorUserID != nil && *e.ActorUserID == user.ID && e.Action == auth.ActionUserUpdated && e.EntityType == "user" && *e.EntityID == targetUser.ID
		})).Return(nil).Once()

		txRunner := &recordingTxRunner{}
		uc := auth.NewUserUseCaseWithTxAndAudit(userRepo, passSvc, nil, 7*24*time.Hour, txRunner, auditRepo)

		newRole := "admin"
		cmd := auth.UpdateUserCommand{
			ActorID:  user.ID,
			TenantID: tenantID,
			UserID:   targetUser.ID,
			Role:     &newRole,
		}

		updated, err := uc.UpdateUser(ctx, cmd)
		req.NoError(err)
		is.NotNil(updated)
		is.True(txRunner.entered, "UpdateUser must execute inside a transaction")
	})

	t.Run("triggers revocation timestamp and session deletion when user is deleted", func(t *testing.T) {
		req := require.New(t)

		activeTarget := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target2@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "editor",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, activeTarget.ID).Return(activeTarget, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.MatchedBy(func(u *domain.User) bool {
			return u.ID == activeTarget.ID && !u.IsActive
		})).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, activeTarget.ID, mock.Anything, 7*24*time.Hour).Return(nil)
		sessionStore.EXPECT().RevokeAllUserSessions(mock.Anything, tenantID, activeTarget.ID).Return(nil)

		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		err := uc.DeleteUser(ctx, user.ID, tenantID, activeTarget.ID)
		req.NoError(err)
	})

	t.Run("deactivation rolls back when revocation fails", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		activeTarget := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target3@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "editor",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, activeTarget.ID).Return(activeTarget, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.Anything).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, activeTarget.ID, mock.Anything, 7*24*time.Hour).Return(assert.AnError)

		runner := &recordingTxRunner{}
		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, 7*24*time.Hour, runner)

		activeState := false
		cmd := auth.UpdateUserCommand{
			ActorID:  user.ID,
			TenantID: tenantID,
			UserID:   activeTarget.ID,
			IsActive: &activeState,
		}

		_, err := uc.UpdateUser(ctx, cmd)
		req.Error(err)
		is.True(runner.entered, "expected transaction runner to be entered")
		is.Error(runner.innerErr, "expected inner error inside transaction runner")
	})

	t.Run("deactivation runs inside a transaction", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		activeTarget := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target5@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "editor",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, activeTarget.ID).Return(activeTarget, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.MatchedBy(func(u *domain.User) bool {
			return u.ID == activeTarget.ID && !u.IsActive
		})).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, activeTarget.ID, mock.Anything, 7*24*time.Hour).Return(nil)
		sessionStore.EXPECT().RevokeAllUserSessions(mock.Anything, tenantID, activeTarget.ID).Return(nil)

		runner := &recordingTxRunner{}
		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, 7*24*time.Hour, runner)

		activeState := false
		cmd := auth.UpdateUserCommand{
			ActorID:  user.ID,
			TenantID: tenantID,
			UserID:   activeTarget.ID,
			IsActive: &activeState,
		}

		updated, err := uc.UpdateUser(ctx, cmd)
		req.NoError(err)
		is.False(updated.IsActive)
		is.True(runner.entered, "expected transaction runner to be entered")
	})

	t.Run("deletion runs inside a transaction", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		activeTarget := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target6@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "editor",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, activeTarget.ID).Return(activeTarget, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.MatchedBy(func(u *domain.User) bool {
			return u.ID == activeTarget.ID && !u.IsActive
		})).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, activeTarget.ID, mock.Anything, 7*24*time.Hour).Return(nil)
		sessionStore.EXPECT().RevokeAllUserSessions(mock.Anything, tenantID, activeTarget.ID).Return(nil)

		runner := &recordingTxRunner{}
		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, 7*24*time.Hour, runner)

		err := uc.DeleteUser(ctx, user.ID, tenantID, activeTarget.ID)
		req.NoError(err)
		is.True(runner.entered, "expected transaction runner to be entered")
	})

	t.Run("triggers revocation on role change", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		activeTarget := &domain.User{
			ID:           uuid.New(),
			TenantID:     tenantID,
			Email:        "target4@flowforge.local",
			PasswordHash: hashedPass,
			Role:         "editor",
			IsActive:     true,
		}

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().FindByID(mock.Anything, tenantID, activeTarget.ID).Return(activeTarget, nil)
		userRepo.EXPECT().UpdateUser(mock.Anything, mock.MatchedBy(func(u *domain.User) bool {
			return u.ID == activeTarget.ID && u.Role == "admin"
		})).Return(nil)

		sessionStore := authmocks.NewMockSessionStore(t)
		sessionStore.EXPECT().SetUserRevokedBefore(mock.Anything, activeTarget.ID, mock.Anything, 7*24*time.Hour).Return(nil)
		sessionStore.EXPECT().RevokeAllUserSessions(mock.Anything, tenantID, activeTarget.ID).Return(nil)

		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		newRole := "admin"
		cmd := auth.UpdateUserCommand{
			ActorID:  user.ID,
			TenantID: tenantID,
			UserID:   activeTarget.ID,
			Role:     &newRole,
		}

		updated, err := uc.UpdateUser(ctx, cmd)
		req.NoError(err)
		is.Equal("admin", updated.Role)
	})
}

func TestUserUseCase_ListUsers(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	passSvc := auth.NewPasswordServiceWithCost(bcrypt.MinCost)

	mockUsers := []*domain.User{
		{ID: uuid.New(), TenantID: tenantID, Email: "user1@flowforge.local", Role: "editor", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, Email: "user2@flowforge.local", Role: "editor", IsActive: true},
		{ID: uuid.New(), TenantID: tenantID, Email: "user3@flowforge.local", Role: "editor", IsActive: true},
	}

	t.Run("returns paginated users list", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		userRepo := authmocks.NewMockUserRepository(t)
		userRepo.EXPECT().ListUsers(mock.Anything, tenantID, 1, 10, "", "", (*bool)(nil), (*bool)(nil)).Return(mockUsers, int64(3), nil)

		uc := auth.NewUserUseCaseWithTx(userRepo, passSvc, nil, 7*24*time.Hour, auth.NewPassthroughTxRunner())

		res, err := uc.ListUsers(ctx, auth.ListUsersQuery{
			TenantID: tenantID,
			Page:     1,
			PageSize: 10,
		})
		req.NoError(err)
		is.Equal(int64(3), res.Total)
		is.Len(res.Users, 3)
	})
}

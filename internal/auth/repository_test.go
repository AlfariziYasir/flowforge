package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"flowforge/internal/auth"
	"flowforge/internal/domain"
)

// MockUserRepository implements auth.UserRepository in-memory for testing logic without DB.
type MockUserRepository struct {
	users map[string]*domain.User
}

func NewMockUserRepository() *MockUserRepository {
	return &MockUserRepository{
		users: make(map[string]*domain.User),
	}
}

func (m *MockUserRepository) FindByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error) {
	sanitizedEmail := strings.ToLower(strings.TrimSpace(email))
	key := tenantID.String() + ":" + sanitizedEmail
	user, exists := m.users[key]
	if !exists {
		return nil, auth.ErrUserNotFound
	}
	return user, nil
}

func (m *MockUserRepository) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.User, error) {
	for _, u := range m.users {
		if u.TenantID == tenantID && u.ID == id {
			return u, nil
		}
	}
	return nil, auth.ErrUserNotFound
}

func (m *MockUserRepository) CreateUser(ctx context.Context, user *domain.User) error {
	sanitizedEmail := strings.ToLower(strings.TrimSpace(user.Email))
	key := user.TenantID.String() + ":" + sanitizedEmail
	if _, exists := m.users[key]; exists {
		return auth.ErrUserAlreadyExists
	}
	user.Email = sanitizedEmail
	m.users[key] = user
	return nil
}

func (m *MockUserRepository) UpdateUser(ctx context.Context, user *domain.User) error {
	sanitizedEmail := strings.ToLower(strings.TrimSpace(user.Email))
	key := user.TenantID.String() + ":" + sanitizedEmail
	if _, exists := m.users[key]; !exists {
		return auth.ErrUserNotFound
	}
	m.users[key] = user
	return nil
}

func TestUserRepository_FindByEmail(t *testing.T) {
	repo := NewMockUserRepository()
	ctx := context.Background()

	tenantID := uuid.New()
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Email:        "Admin@FlowForge.Local",
		PasswordHash: "$2a$12$hash",
		Role:         "admin",
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	err := repo.CreateUser(ctx, user)
	assert.NoError(t, err)

	t.Run("returns user when email and tenant ID match regardless of casing", func(t *testing.T) {
		is := assert.New(t)

		got, err := repo.FindByEmail(ctx, tenantID, "admin@flowforge.local")
		is.NoError(err)
		is.Equal(user.ID, got.ID)
		is.Equal("admin@flowforge.local", got.Email)
	})

	t.Run("returns ErrUserAlreadyExists on duplicate email for same tenant", func(t *testing.T) {
		is := assert.New(t)

		dupUser := &domain.User{
			ID:       uuid.New(),
			TenantID: tenantID,
			Email:    "ADMIN@FLOWFORGE.LOCAL",
		}
		err := repo.CreateUser(ctx, dupUser)
		is.ErrorIs(err, auth.ErrUserAlreadyExists)
	})

	t.Run("returns ErrUserNotFound when email does not exist", func(t *testing.T) {
		is := assert.New(t)

		_, err := repo.FindByEmail(ctx, tenantID, "nonexistent@flowforge.local")
		is.ErrorIs(err, auth.ErrUserNotFound)
	})

	t.Run("enforces tenant isolation — wrong tenant ID returns ErrUserNotFound", func(t *testing.T) {
		is := assert.New(t)

		wrongTenantID := uuid.New()
		_, err := repo.FindByEmail(ctx, wrongTenantID, "admin@flowforge.local")
		is.ErrorIs(err, auth.ErrUserNotFound)
	})
}

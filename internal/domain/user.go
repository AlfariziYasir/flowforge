package domain

import (
	"time"

	"github.com/google/uuid"
)

// User represents a tenant user account.
type User struct {
	ID           uuid.UUID `db:"id" json:"id"`
	TenantID     uuid.UUID `db:"tenant_id" json:"tenantId"`
	Email        string    `db:"email" json:"email"`
	PasswordHash string    `db:"password_hash" json:"-"`
	Role         string    `db:"role" json:"role"`
	IsActive     bool      `db:"is_active" json:"isActive"`
	CreatedAt    time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt    time.Time `db:"updated_at" json:"updatedAt"`
}

func (User) Columns() []string {
	return []string{
		"id",
		"tenant_id",
		"email",
		"password_hash",
		"role",
		"is_active",
		"created_at",
		"updated_at",
	}
}

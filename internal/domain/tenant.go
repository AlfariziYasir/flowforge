package domain

import (
	"time"

	"github.com/google/uuid"
)

// Tenant represents a workspace tenant boundary.
type Tenant struct {
	ID        uuid.UUID `db:"id" json:"id"`
	Slug      string    `db:"slug" json:"slug"`
	Name      string    `db:"name" json:"name"`
	CreatedAt time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt time.Time `db:"updated_at" json:"updatedAt"`
}

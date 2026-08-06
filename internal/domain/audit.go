package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AuditEntry is a tenant-scoped record of a user-visible mutation, written to
// the shared audit_logs table. It is generic across features — nothing here is
// workflow- or execution-specific — so it lives in domain rather than any one
// feature package. EntityType has no default: every caller must set it, so an
// entry can never be silently mislabeled by omission.
type AuditEntry struct {
	ID          uuid.UUID       `db:"id"`
	TenantID    uuid.UUID       `db:"tenant_id"`
	ActorUserID *uuid.UUID      `db:"actor_user_id"`
	Action      string          `db:"action"`
	EntityType  string          `db:"entity_type"`
	EntityID    *uuid.UUID      `db:"entity_id"`
	Metadata    json.RawMessage `db:"metadata"`
	CreatedAt   time.Time       `db:"created_at"`
}

// AuditRepository records audit entries. The concrete Postgres-backed
// implementation lives in internal/platform/audit.
type AuditRepository interface {
	Record(ctx context.Context, e AuditEntry) error
}

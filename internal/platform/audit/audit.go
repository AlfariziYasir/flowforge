// Package audit provides the shared Postgres-backed implementation of
// domain.AuditRepository, used by any feature package that needs to record a
// user-visible mutation against the audit_logs table.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"
)

type postgresAuditRepository struct {
	base *postgres.BaseRepository[domain.AuditEntry]
}

// NewAuditRepository builds the shared audit-log repository.
func NewAuditRepository(pool *pgxpool.Pool) domain.AuditRepository {
	return &postgresAuditRepository{
		base: postgres.NewBaseRepository[domain.AuditEntry](pool, "audit_logs"),
	}
}

func (r *postgresAuditRepository) Record(ctx context.Context, e domain.AuditEntry) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if len(e.Metadata) == 0 {
		e.Metadata = json.RawMessage(`{}`)
	}

	if err := r.base.Create(ctx, &e); err != nil {
		return fmt.Errorf("record audit log: %w", err)
	}
	return nil
}

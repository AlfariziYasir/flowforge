package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"flowforge/internal/platform/postgres"
)

const (
	ActionWorkflowCreated    = "workflow.created"
	ActionWorkflowUpdated    = "workflow.updated"
	ActionWorkflowArchived   = "workflow.archived"
	ActionWorkflowDraftSaved = "workflow.draft_saved"
	ActionWorkflowPublished  = "workflow.published"
	ActionWorkflowRolledBack = "workflow.rolled_back"
)

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

type AuditRepository interface {
	Record(ctx context.Context, e AuditEntry) error
}

type postgresAuditRepository struct {
	base *postgres.BaseRepository[AuditEntry]
}

func NewAuditRepository(pool *pgxpool.Pool) AuditRepository {
	return &postgresAuditRepository{
		base: postgres.NewBaseRepository[AuditEntry](pool, "audit_logs"),
	}
}

func (r *postgresAuditRepository) Record(ctx context.Context, e AuditEntry) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if e.EntityType == "" {
		e.EntityType = "workflow"
	}
	if len(e.Metadata) == 0 {
		e.Metadata = json.RawMessage(`{}`)
	}

	if err := r.base.Create(ctx, &e); err != nil {
		return fmt.Errorf("record audit log: %w", err)
	}
	return nil
}

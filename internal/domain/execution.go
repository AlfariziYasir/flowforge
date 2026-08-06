package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Workflow run status values. These mirror the CHECK constraint on
// workflow_runs.status in migrations/000001_init_schema.up.sql — the constraint
// is the contract. Note the single "l": the database says "canceled".
const (
	RunStatusPending   = "pending"
	RunStatusRunning   = "running"
	RunStatusWaiting   = "waiting"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"
	RunStatusTimedOut  = "timed_out"
)

// WorkflowRun is the tenant-scoped record of one execution of a workflow version.
//
// Nullable columns (started_at, finished_at, idempotency_key, claimed_by,
// lease_expires_at, retried_from_run_id) are pointers — the Phase 3 lesson.
// claimed_by and lease_expires_at are worker claiming state owned by the run
// claim (Phase 5). retried_from_run_id is retry lineage (Phase 6, migration
// 000004) — nil for a run created by a normal trigger.
type WorkflowRun struct {
	ID                uuid.UUID       `db:"id" json:"id"`
	TenantID          uuid.UUID       `db:"tenant_id" json:"tenantId"`
	WorkflowID        uuid.UUID       `db:"workflow_id" json:"workflowId"`
	WorkflowVersionID uuid.UUID       `db:"workflow_version_id" json:"workflowVersionId"`
	Status            string          `db:"status" json:"status"`
	TriggerType       string          `db:"trigger_type" json:"triggerType"`
	IdempotencyKey    *string         `db:"idempotency_key" json:"idempotencyKey"`
	InputContext      json.RawMessage `db:"input_context" json:"inputContext"`
	ExecutionContext  json.RawMessage `db:"execution_context" json:"executionContext"`
	StartedAt         *time.Time      `db:"started_at" json:"startedAt"`
	FinishedAt        *time.Time      `db:"finished_at" json:"finishedAt"`
	ClaimedBy         *string         `db:"claimed_by" json:"-"`
	LeaseExpiresAt    *time.Time      `db:"lease_expires_at" json:"-"`
	RetriedFromRunID  *uuid.UUID      `db:"retried_from_run_id" json:"retriedFromRunId"`
	CreatedAt         time.Time       `db:"created_at" json:"createdAt"`
	UpdatedAt         time.Time       `db:"updated_at" json:"updatedAt"`
}

// StepRun is one step's execution record within a run. status follows the
// engine's StepStatus* constants (internal/engine/state.go).
type StepRun struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	TenantID       uuid.UUID       `db:"tenant_id" json:"tenantId"`
	WorkflowRunID  uuid.UUID       `db:"workflow_run_id" json:"workflowRunId"`
	WorkflowNodeID uuid.UUID       `db:"workflow_node_id" json:"-"`
	NodeKey        string          `db:"node_key" json:"nodeKey"`
	Status         string          `db:"status" json:"status"`
	AttemptCount   int             `db:"attempt_count" json:"attemptCount"`
	InputPayload   json.RawMessage `db:"input_payload" json:"inputPayload"`
	OutputPayload  json.RawMessage `db:"output_payload" json:"outputPayload"`
	ErrorPayload   json.RawMessage `db:"error_payload" json:"errorPayload"`
	StartedAt      *time.Time      `db:"started_at" json:"startedAt"`
	FinishedAt     *time.Time      `db:"finished_at" json:"finishedAt"`
	CreatedAt      time.Time       `db:"created_at" json:"createdAt"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updatedAt"`
}

// Log levels, mirroring the CHECK on execution_logs.level.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// ExecutionLog is a tenant-scoped, run-scoped structured log line. step_run_id
// is nullable because run-level log lines have no step.
type ExecutionLog struct {
	ID            uuid.UUID       `db:"id" json:"id"`
	TenantID      uuid.UUID       `db:"tenant_id" json:"tenantId"`
	WorkflowRunID uuid.UUID       `db:"workflow_run_id" json:"workflowRunId"`
	StepRunID     *uuid.UUID      `db:"step_run_id" json:"stepRunId"`
	Level         string          `db:"level" json:"level"`
	Message       string          `db:"message" json:"message"`
	Context       json.RawMessage `db:"context" json:"context"`
	CreatedAt     time.Time       `db:"created_at" json:"createdAt"`
}

// StepWaitToken parks a run and step on a correlation key while an EVENT_WAIT
// node waits for an inbound event (Phase 7). UNIQUE (tenant_id, correlation_key)
// means a key is held by at most one waiting step; consumed_at is the dedup
// record, so a redelivered event is a safe no-op.
type StepWaitToken struct {
	ID             uuid.UUID  `db:"id" json:"id"`
	TenantID       uuid.UUID  `db:"tenant_id" json:"tenantId"`
	WorkflowRunID  uuid.UUID  `db:"workflow_run_id" json:"workflowRunId"`
	StepRunID      uuid.UUID  `db:"step_run_id" json:"stepRunId"`
	CorrelationKey string     `db:"correlation_key" json:"correlationKey"`
	ExpiresAt      time.Time  `db:"expires_at" json:"expiresAt"`
	ConsumedAt     *time.Time `db:"consumed_at" json:"consumedAt"`
	HandledAt      *time.Time `db:"handled_at" json:"handledAt"`
	CreatedAt      time.Time  `db:"created_at" json:"createdAt"`
}

// OrphanEvent dead-letters an inbound event that matched no wait token. Stored
// rather than dropped so a correlation-key bug is discoverable and countable.
type OrphanEvent struct {
	ID             uuid.UUID       `db:"id" json:"id"`
	TenantID       uuid.UUID       `db:"tenant_id" json:"tenantId"`
	CorrelationKey string          `db:"correlation_key" json:"correlationKey"`
	Payload        json.RawMessage `db:"payload" json:"payload"`
	Reason         string          `db:"reason" json:"reason"`
	ArrivedAt      time.Time       `db:"arrived_at" json:"arrivedAt"`
}

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"
)

var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// ErrRunNotFound is returned when a run does not exist for the tenant.
var ErrRunNotFound = errors.New("workflow run not found")

// ErrStepNotFound is returned when a step run does not exist for the run.
var ErrStepNotFound = errors.New("step run not found")

// ErrLeaseLost is returned by lease operations when the run is no longer held
// by this worker: it finished, or another worker reclaimed it. Either way the
// worker must stop touching the run (the fencing token that makes the lease a
// lease).
var ErrLeaseLost = errors.New("lease no longer held by this worker")

// RunRepository persists workflow runs and performs the atomic run claim.
type RunRepository interface {
	// CreateRun inserts a pending run. created=false with a nil error means the
	// idempotency key already exists — the caller must not seed steps or enqueue.
	CreateRun(ctx context.Context, run *domain.WorkflowRun) (created bool, err error)
	GetRun(ctx context.Context, tenantID, runID uuid.UUID) (*domain.WorkflowRun, error)
	FindByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.WorkflowRun, error)
	// ClaimRun atomically transitions a pending run to running, assigning a worker
	// lease. A nil run with a nil error is the NORMAL outcome of a duplicate queue
	// delivery — the run was already claimed, not an error.
	ClaimRun(ctx context.Context, tenantID, runID uuid.UUID, workerID string, lease time.Duration) (*domain.WorkflowRun, error)
	UpdateRunStatus(ctx context.Context, tenantID, runID uuid.UUID, status string, finished bool) error
	// ExtendLease renews the lease ONLY if this worker still owns the run.
	// It returns ErrLeaseLost when ownership has passed elsewhere or the run
	// finished — the worker must stop.
	ExtendLease(ctx context.Context, tenantID, runID uuid.UUID, workerID string, lease time.Duration) error
	// ReclaimExpiredLeases returns runs whose lease expired while running; the
	// caller must re-enqueue them.
	ReclaimExpiredLeases(ctx context.Context) ([]ReclaimedRun, error)
	// ReclaimStalePendingRuns returns pending runs older than the threshold that
	// were never claimed (their enqueue was lost). Unlike a reclaimed lease, a
	// pending run needs no reset — it is already in the correct pre-execution
	// state; the caller only re-enqueues it. Harmless to re-run every reaper tick:
	// a duplicate delivery is a documented no-op, and the moment a worker claims
	// it the status flips to 'running' and it drops out of this query.
	ReclaimStalePendingRuns(ctx context.Context, olderThan time.Duration) ([]ReclaimedRun, error)
	// ListRuns lists a workflow's runs, newest first, tenant-scoped.
	ListRuns(ctx context.Context, f ListRunsFilter) ([]*domain.WorkflowRun, int64, error)
}

// StepRunRepository persists step runs and performs the atomic step claim.
type StepRunRepository interface {
	CreateStepRuns(ctx context.Context, tenantID, runID uuid.UUID, nodes []domain.WorkflowNode) error
	// ClaimReadySteps claims up to limit ready steps of a run under SKIP LOCKED,
	// so concurrent workers never double-claim. Ordered by node_key for determinism.
	ClaimReadySteps(ctx context.Context, tenantID, runID uuid.UUID, limit int) ([]domain.StepRun, error)
	// MarkStepsReady transitions pending steps the engine deemed ready. It is
	// conditional on status='pending' so a concurrent worker's claim is never
	// overwritten.
	MarkStepsReady(ctx context.Context, tenantID, runID uuid.UUID, nodeKeys []string) error
	UpdateStepStatus(ctx context.Context, tenantID, runID uuid.UUID, nodeKey, status string) error
	UpdateStepResult(ctx context.Context, tenantID, runID uuid.UUID, nodeKey string, u StepUpdate) error
	MarkStepsSkipped(ctx context.Context, tenantID, runID uuid.UUID, nodeKeys []string) error
	ListStepRuns(ctx context.Context, tenantID, runID uuid.UUID) ([]domain.StepRun, error)
	GetStepRun(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error)
	// CloneStepRunsForRetry seeds a retried run's step rows from the original
	// run's. Steps reused wholesale (when onlyFailed is true, the succeeded ones)
	// are copied as 'succeeded' with their original timing and output; every other
	// node gets a fresh 'pending' row, byte-identical to CreateStepRuns.
	CloneStepRunsForRetry(ctx context.Context, tenantID, newRunID, originalRunID uuid.UUID, nodes []domain.WorkflowNode, onlyFailed bool) error
}

// LogRepository persists run/step execution log lines.
type LogRepository interface {
	Append(ctx context.Context, entry *domain.ExecutionLog) error
}

// LogReader lists persisted execution logs for the run-detail API.
type LogReader interface {
	ListLogs(ctx context.Context, f ListLogsFilter) ([]*domain.ExecutionLog, int64, error)
}

// WaitTokenRepository persists the wait tokens EVENT_WAIT steps park on, and
// their resolution. It also owns the orphan-event dead-letter and the tenant
// webhook secret the ingress adapters verify signatures against.
type WaitTokenRepository interface {
	CreateToken(ctx context.Context, token *domain.StepWaitToken) error
	CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error)
	FindTokenByCorrelationKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.StepWaitToken, error)
	// ConsumeTokenIfUnconsumed atomically marks the token consumed. false means
	// a concurrent consumer (or a redelivery) already got it.
	ConsumeTokenIfUnconsumed(ctx context.Context, tokenID uuid.UUID) (bool, error)
	// MarkWaitingStepSucceeded resolves the parked step with the event payload,
	// gated on the step still being 'waiting'.
	MarkWaitingStepSucceeded(ctx context.Context, tenantID, stepRunID uuid.UUID, output []byte) error
	// MarkWaitingStepFailed is the sweeper's expiry path for a token that timed out.
	MarkWaitingStepFailed(ctx context.Context, tenantID, stepRunID uuid.UUID) error
	MarkTokenHandled(ctx context.Context, tenantID, tokenID uuid.UUID) error
	FindExpiredTokens(ctx context.Context, limit int) ([]domain.StepWaitToken, error)
	RecordOrphanEvent(ctx context.Context, e *domain.OrphanEvent) error
	GetWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error)
	SetWebhookSecret(ctx context.Context, tenantID uuid.UUID, secret string) error
}

// ListRunsFilter scopes a run listing. WorkflowID is required — api-3.md 10.1.2
// scopes run lists to a workflow.
type ListRunsFilter struct {
	TenantID       uuid.UUID
	WorkflowID     uuid.UUID
	Status         string // "" = no predicate
	TriggerType    string // "" = no predicate
	CreatedAtFrom  *time.Time
	CreatedAtTo    *time.Time
	Page, PageSize int
}

// ListLogsFilter scopes a log listing. StepRunID is a string so "" means
// unfiltered without an extra bool.
type ListLogsFilter struct {
	TenantID       uuid.UUID
	WorkflowRunID  uuid.UUID
	Level          string
	StepRunID      string
	Page, PageSize int
}

// ReclaimedRun identifies a run whose lease expired and is now pending again.
type ReclaimedRun struct {
	TenantID uuid.UUID
	RunID    uuid.UUID
}

// StepUpdate carries the mutable fields written after a step executes.
type StepUpdate struct {
	Status       string
	AttemptCount int
	Output       []byte
	ErrorPayload []byte
	FinishedAt   *time.Time
}

type executionRepository struct {
	pool *pgxpool.Pool
}

// NewExecutionRepository builds the combined run/step/log repository.
func NewExecutionRepository(pool *pgxpool.Pool) *executionRepository {
	return &executionRepository{pool: pool}
}

func (r *executionRepository) getRunner(ctx context.Context) postgres.DBTX {
	return postgres.GetDBTX(ctx, r.pool)
}

// runColumns is the canonical workflow_runs projection used by every query that
// scans a full row. Adding a column to the table means editing this list once;
// the three Scan sites share it to prevent misalignment.
var runColumns = []string{
	"id", "tenant_id", "workflow_id", "workflow_version_id", "status",
	"trigger_type", "idempotency_key", "input_context", "execution_context",
	"started_at", "finished_at", "claimed_by", "lease_expires_at",
	"retried_from_run_id", "created_at", "updated_at",
}

// scanRun scans a single workflow_runs row using the canonical projection.
func scanRun(row interface{ Scan(dest ...any) error }) (*domain.WorkflowRun, error) {
	var run domain.WorkflowRun
	err := row.Scan(
		&run.ID, &run.TenantID, &run.WorkflowID, &run.WorkflowVersionID, &run.Status,
		&run.TriggerType, &run.IdempotencyKey, &run.InputContext, &run.ExecutionContext,
		&run.StartedAt, &run.FinishedAt, &run.ClaimedBy, &run.LeaseExpiresAt,
		&run.RetriedFromRunID, &run.CreatedAt, &run.UpdatedAt,
	)
	return &run, err
}

func (r *executionRepository) CreateRun(ctx context.Context, run *domain.WorkflowRun) (bool, error) {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.Status == "" {
		run.Status = domain.RunStatusPending
	}
	if run.TriggerType == "" {
		run.TriggerType = "manual"
	}
	if len(run.InputContext) == 0 {
		run.InputContext = json.RawMessage(`{}`)
	}
	if len(run.ExecutionContext) == 0 {
		run.ExecutionContext = json.RawMessage(`{}`)
	}
	now := time.Now()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}

	query, args, err := psql.Insert("workflow_runs").
		Columns("id", "tenant_id", "workflow_id", "workflow_version_id", "status",
			"trigger_type", "idempotency_key", "input_context", "execution_context",
			"started_at", "finished_at", "retried_from_run_id", "created_at", "updated_at").
		Values(run.ID, run.TenantID, run.WorkflowID, run.WorkflowVersionID, run.Status,
			run.TriggerType, run.IdempotencyKey, run.InputContext, run.ExecutionContext,
			run.StartedAt, run.FinishedAt, run.RetriedFromRunID, run.CreatedAt, run.UpdatedAt).
		Suffix("ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING").
		ToSql()
	if err != nil {
		return false, fmt.Errorf("build insert run: %w", err)
	}
	tag, err := r.getRunner(ctx).Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("insert workflow run: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *executionRepository) GetRun(ctx context.Context, tenantID, runID uuid.UUID) (*domain.WorkflowRun, error) {
	query, args, err := psql.Select(runColumns...).
		From("workflow_runs").
		Where(sq.Eq{"tenant_id": tenantID, "id": runID}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build select run: %w", err)
	}

	run, err := scanRun(r.getRunner(ctx).QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("scan run: %w", err)
	}
	return run, nil
}

func (r *executionRepository) FindByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.WorkflowRun, error) {
	query, args, err := psql.Select(runColumns...).
		From("workflow_runs").
		Where(sq.Eq{"tenant_id": tenantID, "idempotency_key": key}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build select run by idempotency key: %w", err)
	}

	run, err := scanRun(r.getRunner(ctx).QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("scan run by idempotency key: %w", err)
	}
	return run, nil
}

func (r *executionRepository) ClaimRun(ctx context.Context, tenantID, runID uuid.UUID, workerID string, lease time.Duration) (*domain.WorkflowRun, error) {
	query := `UPDATE workflow_runs
		SET status = 'running', started_at = NOW(), updated_at = NOW(),
		    claimed_by = $2, lease_expires_at = NOW() + $3::interval
		WHERE id = $1 AND tenant_id = $4 AND status = 'pending'
		RETURNING ` + strings.Join(runColumns, ", ")

	run, err := scanRun(r.getRunner(ctx).QueryRow(ctx, query, runID, workerID, lease.String(), tenantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim run: %w", err)
	}
	return run, nil
}

func (r *executionRepository) UpdateRunStatus(ctx context.Context, tenantID, runID uuid.UUID, status string, finished bool) error {
	b := psql.Update("workflow_runs").
		Set("status", status).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "id": runID})
	if finished {
		b = b.Set("finished_at", sq.Expr("NOW()"))
	}
	query, args, err := b.ToSql()
	if err != nil {
		return fmt.Errorf("build update run status: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

func (r *executionRepository) ExtendLease(ctx context.Context, tenantID, runID uuid.UUID, workerID string, lease time.Duration) error {
	query, args, err := psql.Update("workflow_runs").
		Set("lease_expires_at", sq.Expr("NOW() + ?::interval", lease.String())).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "id": runID, "status": domain.RunStatusRunning, "claimed_by": workerID}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build extend lease: %w", err)
	}
	tag, err := r.getRunner(ctx).Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("extend lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (r *executionRepository) ReclaimExpiredLeases(ctx context.Context) ([]ReclaimedRun, error) {
	// One statement: the run reset and the orphaned-step reset must not be
	// separable. A run returned to 'pending' while its steps stay 'running' is
	// not recoverable (claims only target ready/retrying). Orphaned steps become
	// 'retrying' with attempt_count incremented so RetryPolicy.MaxAttempts can
	// eventually give up on a step that crashes its worker every time.
	query := `WITH reclaimed AS (
		UPDATE workflow_runs
		   SET status = 'pending', claimed_by = NULL, lease_expires_at = NULL, updated_at = NOW()
		 WHERE status = 'running' AND lease_expires_at < NOW()
		RETURNING id, tenant_id
	),
	reset_steps AS (
		UPDATE step_runs s
		   SET status = 'retrying', attempt_count = s.attempt_count + 1, updated_at = NOW()
		  FROM reclaimed r
		 WHERE s.workflow_run_id = r.id AND s.tenant_id = r.tenant_id
		   AND s.status IN ('running', 'ready')
	)
	SELECT id, tenant_id FROM reclaimed`

	rows, err := r.getRunner(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reclaim expired leases: %w", err)
	}
	defer rows.Close()

	var reclaimed []ReclaimedRun
	for rows.Next() {
		var rec ReclaimedRun
		if err := rows.Scan(&rec.RunID, &rec.TenantID); err != nil {
			return nil, fmt.Errorf("scan reclaimed run: %w", err)
		}
		reclaimed = append(reclaimed, rec)
	}
	return reclaimed, rows.Err()
}

func (r *executionRepository) ReclaimStalePendingRuns(ctx context.Context, olderThan time.Duration) ([]ReclaimedRun, error) {
	query := `SELECT id, tenant_id FROM workflow_runs
	          WHERE status = 'pending' AND claimed_by IS NULL AND created_at < NOW() - $1::interval`
	rows, err := r.getRunner(ctx).Query(ctx, query, olderThan.String())
	if err != nil {
		return nil, fmt.Errorf("reclaim stale pending runs: %w", err)
	}
	defer rows.Close()

	var stale []ReclaimedRun
	for rows.Next() {
		var rec ReclaimedRun
		if err := rows.Scan(&rec.RunID, &rec.TenantID); err != nil {
			return nil, fmt.Errorf("scan stale pending run: %w", err)
		}
		stale = append(stale, rec)
	}
	return stale, rows.Err()
}

func (r *executionRepository) ListRuns(ctx context.Context, f ListRunsFilter) ([]*domain.WorkflowRun, int64, error) {
	cond := sq.Eq{"tenant_id": f.TenantID, "workflow_id": f.WorkflowID}
	if f.Status != "" {
		cond["status"] = f.Status
	}
	if f.TriggerType != "" {
		cond["trigger_type"] = f.TriggerType
	}

	page, pageSize := f.Page, f.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	} else if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	b := psql.Select(runColumns...).
		From("workflow_runs").
		Where(cond).
		OrderBy("created_at DESC") // api-3.md: newest first; this endpoint has no sortBy
	if f.CreatedAtFrom != nil {
		b = b.Where(sq.GtOrEq{"created_at": *f.CreatedAtFrom})
	}
	if f.CreatedAtTo != nil {
		b = b.Where(sq.LtOrEq{"created_at": *f.CreatedAtTo})
	}
	itemQuery, itemArgs, err := b.Limit(uint64(pageSize)).Offset(uint64(offset)).ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("build list runs: %w", err)
	}

	rows, err := r.getRunner(ctx).Query(ctx, itemQuery, itemArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list runs: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*domain.WorkflowRun, error) {
		return scanRun(row)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan runs: %w", err)
	}

	// Count query mirrors the item query's WHERE exactly.
	countB := psql.Select("COUNT(*)").From("workflow_runs").Where(cond)
	if f.CreatedAtFrom != nil {
		countB = countB.Where(sq.GtOrEq{"created_at": *f.CreatedAtFrom})
	}
	if f.CreatedAtTo != nil {
		countB = countB.Where(sq.LtOrEq{"created_at": *f.CreatedAtTo})
	}
	countQuery, countArgs, err := countB.ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("build count runs: %w", err)
	}
	var total int64
	if err := r.getRunner(ctx).QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count runs: %w", err)
	}

	return items, total, nil
}

func (r *executionRepository) CreateStepRuns(ctx context.Context, tenantID, runID uuid.UUID, nodes []domain.WorkflowNode) error {
	if len(nodes) == 0 {
		return nil
	}
	b := psql.Insert("step_runs").
		Columns("id", "tenant_id", "workflow_run_id", "workflow_node_id", "node_key",
			"status", "attempt_count", "input_payload", "output_payload", "created_at", "updated_at")
	now := time.Now()
	for _, n := range nodes {
		b = b.Values(uuid.New(), tenantID, runID, n.ID, n.NodeKey,
			"pending", 0, "{}", "{}", now, now)
	}
	query, args, err := b.ToSql()
	if err != nil {
		return fmt.Errorf("build insert step runs: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("insert step runs: %w", err)
	}
	return nil
}

// ClaimReadySteps atomically claims up to limit claimable steps of a run and
// transitions them to running in one statement. Claimable means 'ready'
// (engine-scheduled) or 'retrying' (a reaper-reclaimed orphan awaiting
// re-dispatch). The SKIP LOCKED subquery means concurrent workers can never
// double-claim: a step is claimed exactly once. The ready/retrying->running
// transition is enforced by the WHERE clause, not by the caller.
func (r *executionRepository) ClaimReadySteps(ctx context.Context, tenantID, runID uuid.UUID, limit int) ([]domain.StepRun, error) {
	query := `UPDATE step_runs
	          SET status = 'running', started_at = NOW(), updated_at = NOW()
	          WHERE id IN (
	              SELECT id FROM step_runs
	              WHERE tenant_id = $1 AND workflow_run_id = $2 AND status IN ('ready', 'retrying')
	              ORDER BY node_key
	              FOR UPDATE SKIP LOCKED
	              LIMIT $3
	          )
	          RETURNING id, tenant_id, workflow_run_id, workflow_node_id, node_key, status,
	                    attempt_count, input_payload, output_payload, error_payload,
	                    started_at, finished_at, created_at, updated_at`

	rows, err := r.getRunner(ctx).Query(ctx, query, tenantID, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("claim ready steps: %w", err)
	}
	defer rows.Close()

	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.StepRun, error) {
		var s domain.StepRun
		err := row.Scan(
			&s.ID, &s.TenantID, &s.WorkflowRunID, &s.WorkflowNodeID, &s.NodeKey, &s.Status,
			&s.AttemptCount, &s.InputPayload, &s.OutputPayload, &s.ErrorPayload,
			&s.StartedAt, &s.FinishedAt, &s.CreatedAt, &s.UpdatedAt,
		)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan claimed steps: %w", err)
	}
	return steps, nil
}

func (r *executionRepository) MarkStepsReady(ctx context.Context, tenantID, runID uuid.UUID, nodeKeys []string) error {
	if len(nodeKeys) == 0 {
		return nil
	}
	query, args, err := psql.Update("step_runs").
		Set("status", "ready").
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "workflow_run_id": runID}).
		Where(sq.Eq{"node_key": nodeKeys}).
		Where(sq.Eq{"status": "pending"}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build mark ready: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("mark steps ready: %w", err)
	}
	return nil
}

func (r *executionRepository) UpdateStepStatus(ctx context.Context, tenantID, runID uuid.UUID, nodeKey, status string) error {
	query, args, err := psql.Update("step_runs").
		Set("status", status).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "workflow_run_id": runID, "node_key": nodeKey}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build update step status: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("update step status: %w", err)
	}
	return nil
}

func (r *executionRepository) UpdateStepResult(ctx context.Context, tenantID, runID uuid.UUID, nodeKey string, u StepUpdate) error {
	b := psql.Update("step_runs").
		Set("status", u.Status).
		Set("attempt_count", u.AttemptCount).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "workflow_run_id": runID, "node_key": nodeKey})
	if u.Output != nil {
		b = b.Set("output_payload", u.Output)
	}
	if u.ErrorPayload != nil {
		b = b.Set("error_payload", u.ErrorPayload)
	}
	if u.FinishedAt != nil {
		b = b.Set("finished_at", u.FinishedAt)
	}
	query, args, err := b.ToSql()
	if err != nil {
		return fmt.Errorf("build update step result: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("update step result: %w", err)
	}
	return nil
}

func (r *executionRepository) MarkStepsSkipped(ctx context.Context, tenantID, runID uuid.UUID, nodeKeys []string) error {
	if len(nodeKeys) == 0 {
		return nil
	}
	query, args, err := psql.Update("step_runs").
		Set("status", "skipped").
		Set("finished_at", sq.Expr("NOW()")).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "workflow_run_id": runID}).
		Where(sq.Eq{"node_key": nodeKeys}).
		Where(sq.Eq{"status": "pending"}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build mark skipped: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("mark steps skipped: %w", err)
	}
	return nil
}

func (r *executionRepository) ListStepRuns(ctx context.Context, tenantID, runID uuid.UUID) ([]domain.StepRun, error) {
	query, args, err := psql.Select(
		"id", "tenant_id", "workflow_run_id", "workflow_node_id", "node_key", "status",
		"attempt_count", "input_payload", "output_payload", "error_payload",
		"started_at", "finished_at", "created_at", "updated_at").
		From("step_runs").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_run_id": runID}).
		OrderBy("node_key").
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build list step runs: %w", err)
	}

	rows, err := r.getRunner(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list step runs: %w", err)
	}
	defer rows.Close()

	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.StepRun, error) {
		var s domain.StepRun
		err := row.Scan(
			&s.ID, &s.TenantID, &s.WorkflowRunID, &s.WorkflowNodeID, &s.NodeKey, &s.Status,
			&s.AttemptCount, &s.InputPayload, &s.OutputPayload, &s.ErrorPayload,
			&s.StartedAt, &s.FinishedAt, &s.CreatedAt, &s.UpdatedAt,
		)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan step runs: %w", err)
	}
	return steps, nil
}

func (r *executionRepository) GetStepRun(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error) {
	query, args, err := psql.Select(
		"id", "tenant_id", "workflow_run_id", "workflow_node_id", "node_key", "status",
		"attempt_count", "input_payload", "output_payload", "error_payload",
		"started_at", "finished_at", "created_at", "updated_at").
		From("step_runs").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_run_id": runID, "id": stepRunID}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build get step run: %w", err)
	}

	row := r.getRunner(ctx).QueryRow(ctx, query, args...)
	var s domain.StepRun
	if err := row.Scan(
		&s.ID, &s.TenantID, &s.WorkflowRunID, &s.WorkflowNodeID, &s.NodeKey, &s.Status,
		&s.AttemptCount, &s.InputPayload, &s.OutputPayload, &s.ErrorPayload,
		&s.StartedAt, &s.FinishedAt, &s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrStepNotFound
		}
		return nil, fmt.Errorf("scan step run: %w", err)
	}
	return &s, nil
}

// CloneStepRunsForRetry seeds a retried run's step rows from the original run's.
// Steps reused wholesale (onlyFailed && original succeeded) are copied as
// 'succeeded' with their input/output/attempt_count AND original started_at and
// finished_at — a reused step shows the timing of the work it actually did, so
// the run detail view is not misleading. Every other node gets a fresh
// 'pending' row, byte-identical to CreateStepRuns.
func (r *executionRepository) CloneStepRunsForRetry(ctx context.Context, tenantID, newRunID, originalRunID uuid.UUID, nodes []domain.WorkflowNode, onlyFailed bool) error {
	if len(nodes) == 0 {
		return nil
	}

	origSteps, err := r.ListStepRuns(ctx, tenantID, originalRunID)
	if err != nil {
		return fmt.Errorf("clone step runs: load original: %w", err)
	}
	origByKey := make(map[string]domain.StepRun, len(origSteps))
	for _, s := range origSteps {
		origByKey[s.NodeKey] = s
	}

	now := time.Now()
	b := psql.Insert("step_runs").
		Columns("id", "tenant_id", "workflow_run_id", "workflow_node_id", "node_key",
			"status", "attempt_count", "input_payload", "output_payload", "error_payload",
			"started_at", "finished_at", "created_at", "updated_at")

	for _, n := range nodes {
		if onlyFailed {
			if orig, ok := origByKey[n.NodeKey]; ok && orig.Status == "succeeded" {
				b = b.Values(uuid.New(), tenantID, newRunID, n.ID, n.NodeKey,
					"succeeded", orig.AttemptCount, nilIfEmpty(orig.InputPayload), nilIfEmpty(orig.OutputPayload), orig.ErrorPayload,
					orig.StartedAt, orig.FinishedAt, now, now)
				continue
			}
		}
		b = b.Values(uuid.New(), tenantID, newRunID, n.ID, n.NodeKey,
			"pending", 0, "{}", "{}", nil, nil, nil, now, now)
	}

	query, args, err := b.ToSql()
	if err != nil {
		return fmt.Errorf("build clone step runs: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("clone step runs: %w", err)
	}
	return nil
}

func nilIfEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func (r *executionRepository) ListLogs(ctx context.Context, f ListLogsFilter) ([]*domain.ExecutionLog, int64, error) {
	cond := sq.Eq{"tenant_id": f.TenantID, "workflow_run_id": f.WorkflowRunID}
	if f.Level != "" {
		cond["level"] = f.Level
	}
	if f.StepRunID != "" {
		cond["step_run_id"] = f.StepRunID
	}

	page, pageSize := f.Page, f.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	} else if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	itemQuery, itemArgs, err := psql.Select("id", "tenant_id", "workflow_run_id", "step_run_id",
		"level", "message", "context", "created_at").
		From("execution_logs").
		Where(cond).
		OrderBy("created_at DESC").
		Limit(uint64(pageSize)).Offset(uint64(offset)).
		ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("build list logs: %w", err)
	}

	rows, err := r.getRunner(ctx).Query(ctx, itemQuery, itemArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list logs: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*domain.ExecutionLog, error) {
		var l domain.ExecutionLog
		err := row.Scan(&l.ID, &l.TenantID, &l.WorkflowRunID, &l.StepRunID,
			&l.Level, &l.Message, &l.Context, &l.CreatedAt)
		return &l, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan logs: %w", err)
	}

	countQuery, countArgs, err := psql.Select("COUNT(*)").From("execution_logs").Where(cond).ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("build count logs: %w", err)
	}
	var total int64
	if err := r.getRunner(ctx).QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count logs: %w", err)
	}

	return items, total, nil
}

func (r *executionRepository) Append(ctx context.Context, entry *domain.ExecutionLog) error {
	ctxPayload := entry.Context
	if len(ctxPayload) == 0 {
		ctxPayload = json.RawMessage("{}")
	}
	query, args, err := psql.Insert("execution_logs").
		Columns("id", "tenant_id", "workflow_run_id", "step_run_id", "level", "message", "context", "created_at").
		Values(uuid.New(), entry.TenantID, entry.WorkflowRunID, entry.StepRunID, entry.Level,
			entry.Message, ctxPayload, time.Now()).
		ToSql()
	if err != nil {
		return fmt.Errorf("build insert log: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("append log: %w", err)
	}
	return nil
}

// ErrTokenNotFound is returned when no wait token matches a correlation key.
var ErrTokenNotFound = errors.New("wait token not found")

func (r *executionRepository) CreateToken(ctx context.Context, token *domain.StepWaitToken) error {
	if token.ID == uuid.Nil {
		token.ID = uuid.New()
	}
	query, args, err := psql.Insert("step_wait_tokens").
		Columns("id", "tenant_id", "workflow_run_id", "step_run_id", "correlation_key", "expires_at", "consumed_at", "created_at").
		Values(token.ID, token.TenantID, token.WorkflowRunID, token.StepRunID, token.CorrelationKey,
			token.ExpiresAt, token.ConsumedAt, time.Now()).
		ToSql()
	if err != nil {
		return fmt.Errorf("build insert wait token: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("insert wait token: %w", err)
	}
	return nil
}

func (r *executionRepository) CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error) {
	query, args, err := psql.Select("COUNT(*)").From("step_wait_tokens").
		Where(sq.Eq{"tenant_id": tenantID}).Where("consumed_at IS NULL AND handled_at IS NULL").ToSql()
	if err != nil {
		return 0, fmt.Errorf("build count wait tokens: %w", err)
	}
	var n int
	if err := r.getRunner(ctx).QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count wait tokens: %w", err)
	}
	return n, nil
}

func (r *executionRepository) FindTokenByCorrelationKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.StepWaitToken, error) {
	query, args, err := psql.Select("id", "tenant_id", "workflow_run_id", "step_run_id",
		"correlation_key", "expires_at", "consumed_at", "handled_at", "created_at").
		From("step_wait_tokens").
		Where(sq.Eq{"tenant_id": tenantID, "correlation_key": key}).
		OrderBy("created_at DESC").
		Limit(1).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build find wait token: %w", err)
	}
	var t domain.StepWaitToken
	if err := r.getRunner(ctx).QueryRow(ctx, query, args...).Scan(
		&t.ID, &t.TenantID, &t.WorkflowRunID, &t.StepRunID,
		&t.CorrelationKey, &t.ExpiresAt, &t.ConsumedAt, &t.HandledAt, &t.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("scan wait token: %w", err)
	}
	return &t, nil
}

func (r *executionRepository) ConsumeTokenIfUnconsumed(ctx context.Context, tokenID uuid.UUID) (bool, error) {
	query := `UPDATE step_wait_tokens SET consumed_at = NOW(), handled_at = NOW()
	          WHERE id = $1 AND consumed_at IS NULL AND handled_at IS NULL`
	tag, err := r.getRunner(ctx).Exec(ctx, query, tokenID)
	if err != nil {
		return false, fmt.Errorf("consume wait token: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *executionRepository) MarkWaitingStepSucceeded(ctx context.Context, tenantID, stepRunID uuid.UUID, output []byte) error {
	query := `UPDATE step_runs SET status = 'succeeded', output_payload = $3, finished_at = NOW(), updated_at = NOW()
	          WHERE tenant_id = $1 AND id = $2 AND status = 'waiting'`
	tag, err := r.getRunner(ctx).Exec(ctx, query, tenantID, stepRunID, output)
	if err != nil {
		return fmt.Errorf("mark waiting step succeeded: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStepNotFound // the step moved on before we got here — treat as handled upstream
	}
	return nil
}

func (r *executionRepository) MarkWaitingStepFailed(ctx context.Context, tenantID, stepRunID uuid.UUID) error {
	query := `UPDATE step_runs SET status = 'failed', error_payload = '{"message":"wait token expired"}', finished_at = NOW(), updated_at = NOW()
	          WHERE tenant_id = $1 AND id = $2 AND status = 'waiting'`
	tag, err := r.getRunner(ctx).Exec(ctx, query, tenantID, stepRunID)
	if err != nil {
		return fmt.Errorf("mark waiting step failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStepNotFound
	}
	return nil
}

func (r *executionRepository) MarkTokenHandled(ctx context.Context, tenantID, tokenID uuid.UUID) error {
	query := `UPDATE step_wait_tokens SET handled_at = NOW() WHERE tenant_id = $1 AND id = $2 AND handled_at IS NULL`
	_, err := r.getRunner(ctx).Exec(ctx, query, tenantID, tokenID)
	if err != nil {
		return fmt.Errorf("mark token handled: %w", err)
	}
	return nil
}

func (r *executionRepository) FindExpiredTokens(ctx context.Context, limit int) ([]domain.StepWaitToken, error) {
	if limit <= 0 {
		limit = 100
	}
	query, args, err := psql.Select("id", "tenant_id", "workflow_run_id", "step_run_id",
		"correlation_key", "expires_at", "consumed_at", "handled_at", "created_at").
		From("step_wait_tokens").
		Where("consumed_at IS NULL AND handled_at IS NULL").Where("expires_at < NOW()").
		OrderBy("expires_at").
		Limit(uint64(limit)).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build find expired tokens: %w", err)
	}
	rows, err := r.getRunner(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find expired tokens: %w", err)
	}
	defer rows.Close()
	var out []domain.StepWaitToken
	for rows.Next() {
		var t domain.StepWaitToken
		if err := rows.Scan(&t.ID, &t.TenantID, &t.WorkflowRunID, &t.StepRunID,
			&t.CorrelationKey, &t.ExpiresAt, &t.ConsumedAt, &t.HandledAt, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan expired token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *executionRepository) RecordOrphanEvent(ctx context.Context, e *domain.OrphanEvent) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	query, args, err := psql.Insert("orphan_events").
		Columns("id", "tenant_id", "correlation_key", "payload", "reason", "arrived_at").
		Values(e.ID, e.TenantID, e.CorrelationKey, e.Payload, e.Reason, time.Now()).
		ToSql()
	if err != nil {
		return fmt.Errorf("build insert orphan event: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("insert orphan event: %w", err)
	}
	return nil
}

func (r *executionRepository) GetWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	query, args, err := psql.Select("webhook_secret").From("tenants").
		Where(sq.Eq{"id": tenantID}).ToSql()
	if err != nil {
		return "", fmt.Errorf("build get webhook secret: %w", err)
	}
	var secret *string
	if err := r.getRunner(ctx).QueryRow(ctx, query, args...).Scan(&secret); err != nil {
		return "", fmt.Errorf("get webhook secret: %w", err)
	}
	if secret == nil {
		return "", nil
	}
	return *secret, nil
}

func (r *executionRepository) SetWebhookSecret(ctx context.Context, tenantID uuid.UUID, secret string) error {
	query, args, err := psql.Update("tenants").
		Set("webhook_secret", secret).
		Where(sq.Eq{"id": tenantID}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build set webhook secret: %w", err)
	}
	if _, err := r.getRunner(ctx).Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("set webhook secret: %w", err)
	}
	return nil
}

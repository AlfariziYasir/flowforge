package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/platform/ai"
)

// Sentinel errors for the run lifecycle use cases.
var (
	ErrNoPublishedVersion  = errors.New("workflow has no published version to run")
	ErrRunNotRunning       = errors.New("run is not in running state")
	ErrRunIllegalCancel    = errors.New("cannot cancel a run that is not running")
	ErrRunAlreadyRunning   = errors.New("run is already running or pending")
	ErrRunAlreadyCompleted = errors.New("run already reached a terminal status")
	ErrAIInvalidResponse   = errors.New("ai provider returned an invalid response")
	ErrAIGenerationFailed  = errors.New("ai provider failed to generate a response")
)

// TxRunner is satisfied structurally by postgres.UnitOfWork. Declared here so
// the application layer does not depend on an infrastructure package.
type TxRunner interface {
	ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// WorkflowReader fetches a workflow's current (published) version pointer.
type WorkflowReader interface {
	GetWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.Workflow, error)
}

// RunEnqueuer wakes the worker queue for a run.
type RunEnqueuer interface {
	EnqueueRun(tenantID, runID uuid.UUID) error
}

// ExecutionUseCase is the seam Phase 6's HTTP handlers call.
type ExecutionUseCase interface {
	CreateRun(ctx context.Context, cmd CreateRunCommand) (*domain.WorkflowRun, error)
	RetryRun(ctx context.Context, cmd RetryRunCommand) (*domain.WorkflowRun, error)
	CancelRun(ctx context.Context, tenantID, runID uuid.UUID) error
	GetRun(ctx context.Context, tenantID, runID uuid.UUID) (*domain.WorkflowRun, error)
	ListRuns(ctx context.Context, q ListRunsQuery) (*PaginatedRuns, error)
	ListSteps(ctx context.Context, tenantID, runID uuid.UUID) ([]*domain.StepRun, error)
	GetStep(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error)
	ListLogs(ctx context.Context, q ListLogsQuery) (*PaginatedLogs, error)
	AnalyzeRun(ctx context.Context, cmd AnalyzeRunCommand) (*AnalysisResult, error)

	// HandleEvent is the single business-logic entry point for every ingress
	// transport (HTTP webhook, gRPC, NATS). It resolves the wait token for the
	// correlation key, or reports resolved=false so the adapter dead-letters the
	// event as an orphan. A redelivered event for an already-consumed token is a
	// safe no-op with resolved=true.
	HandleEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) (resolved bool, err error)
	// RecordOrphanEvent dead-letters an event that matched no wait token.
	RecordOrphanEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte, reason string) error
	// GetWebhookSecret returns the tenant's event-ingress signing secret ("" if
	// none is configured). RotateWebhookSecret issues a fresh one.
	GetWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error)
	RotateWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error)
}

type executionUseCase struct {
	workflows WorkflowReader
	runs      RunRepository
	steps     StepRunRepository
	logs      LogReader
	graph     GraphLoader
	queue     RunEnqueuer
	audit     domain.AuditRepository
	txRunner  TxRunner
	ai        ai.Provider
	*EventService
	cfg ExecutionConfig
}

// NewExecutionUseCase wires the run lifecycle and analysis features.
func NewExecutionUseCase(
	workflows WorkflowReader, runs RunRepository, steps StepRunRepository, logs LogReader,
	graph GraphLoader, queue RunEnqueuer, audit domain.AuditRepository, txRunner TxRunner,
	aiProvider ai.Provider, tokens WaitTokenRepository, cfg ExecutionConfig,
) ExecutionUseCase {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &executionUseCase{
		workflows:    workflows,
		runs:         runs,
		steps:        steps,
		logs:         logs,
		graph:        graph,
		queue:        queue,
		audit:        audit,
		txRunner:     txRunner,
		ai:           aiProvider,
		EventService: NewEventService(tokens, steps, queue, txRunner, cfg.Logger),
		cfg:          cfg,
	}
}

// CreateRun records a pending run for the workflow's current published version,
// seeds its step rows, and wakes the worker queue. It is idempotent on
// IdempotencyKey: a duplicate key returns the existing run without seeding
// steps, auditing, or enqueueing again.
//
// Enqueueing happens AFTER the transaction commits — never inside it — so a
// rolled-back transaction never leaves an orphaned queue message. A run that
// cannot be enqueued is still returned: the reaper's ReclaimStalePendingRuns
// recovers it.
func (uc *executionUseCase) CreateRun(ctx context.Context, cmd CreateRunCommand) (*domain.WorkflowRun, error) {
	if cmd.TenantID == uuid.Nil || cmd.WorkflowID == uuid.Nil {
		return nil, fmt.Errorf("create run: tenant and workflow are required")
	}

	wf, err := uc.workflows.GetWorkflow(ctx, cmd.TenantID, cmd.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	if wf.CurrentVersionID == nil {
		return nil, ErrNoPublishedVersion
	}

	nodes, _, err := uc.graph.LoadGraph(ctx, cmd.TenantID, *wf.CurrentVersionID)
	if err != nil {
		return nil, fmt.Errorf("create run: load graph: %w", err)
	}

	triggerType := cmd.TriggerType
	if triggerType == "" {
		triggerType = "manual"
	}
	input := cmd.InputContext
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}

	var run *domain.WorkflowRun
	created := false
	err = uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		candidate := &domain.WorkflowRun{
			ID:                uuid.New(),
			TenantID:          cmd.TenantID,
			WorkflowID:        cmd.WorkflowID,
			WorkflowVersionID: *wf.CurrentVersionID,
			Status:            domain.RunStatusPending,
			TriggerType:       triggerType,
			IdempotencyKey:    cmd.IdempotencyKey,
			InputContext:      input,
		}

		ok, err := uc.runs.CreateRun(txCtx, candidate)
		if err != nil {
			return err
		}
		if !ok {
			// Idempotency-key conflict: nothing new happened. Return the existing
			// run; do not seed steps, audit, or enqueue.
			existing, err := uc.runs.FindByIdempotencyKey(txCtx, cmd.TenantID, *cmd.IdempotencyKey)
			if err != nil {
				return err
			}
			run = existing
			return nil
		}
		created = true
		if err := uc.steps.CreateStepRuns(txCtx, cmd.TenantID, candidate.ID, nodes); err != nil {
			return fmt.Errorf("seed steps: %w", err)
		}
		if err := uc.audit.Record(txCtx, domain.AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionRunCreated,
			EntityType:  "workflow_run",
			EntityID:    &candidate.ID,
			Metadata:    json.RawMessage(`{}`),
		}); err != nil {
			return err
		}
		run = candidate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	if created {
		uc.publishEvent(ctx, domain.Event{
			Type:      domain.EventRunCreated,
			TenantID:  cmd.TenantID,
			RunID:     &run.ID,
			Status:    domain.RunStatusPending,
			Timestamp: time.Now().UTC(),
		})
		if err := uc.queue.EnqueueRun(cmd.TenantID, run.ID); err != nil {
			// Not fatal to the request: the run row exists and ReclaimStalePendingRuns
			// will re-enqueue it — but an operator must be able to see the blip.
			uc.cfg.Logger.Warn("enqueue failed after create, relying on stale-pending reclaim",
				slog.String("runID", run.ID.String()), slog.Any("error", err))
			return run, nil
		}
		uc.publishEvent(ctx, domain.Event{
			Type:      domain.EventRunQueued,
			TenantID:  cmd.TenantID,
			RunID:     &run.ID,
			Status:    domain.RunStatusPending,
			Timestamp: time.Now().UTC(),
		})
	}
	return run, nil
}

// RetryRun creates a new run that replays a finished run. succeeded runs are
// rejected as already-completed; pending/running are rejected as already
// running. The new run uses the ORIGINAL run's version (not the workflow's
// current one) so a retry replays the same graph it failed on. Failed steps can
// be re-executed while succeeded steps are copied wholesale.
func (uc *executionUseCase) RetryRun(ctx context.Context, cmd RetryRunCommand) (*domain.WorkflowRun, error) {
	original, err := uc.runs.GetRun(ctx, cmd.TenantID, cmd.RunID)
	if err != nil {
		return nil, err
	}
	switch original.Status {
	case domain.RunStatusSucceeded:
		return nil, ErrRunAlreadyCompleted
	case domain.RunStatusPending, domain.RunStatusRunning:
		return nil, ErrRunAlreadyRunning
	}

	nodes, _, err := uc.graph.LoadGraph(ctx, cmd.TenantID, original.WorkflowVersionID)
	if err != nil {
		return nil, fmt.Errorf("retry run: load graph: %w", err)
	}

	var newRun *domain.WorkflowRun
	created := false
	err = uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		candidate := &domain.WorkflowRun{
			ID:                uuid.New(),
			TenantID:          original.TenantID,
			WorkflowID:        original.WorkflowID,
			WorkflowVersionID: original.WorkflowVersionID,
			Status:            domain.RunStatusPending,
			TriggerType:       original.TriggerType,
			IdempotencyKey:    cmd.IdempotencyKey,
			InputContext:      original.InputContext,
			RetriedFromRunID:  &original.ID,
		}
		ok, err := uc.runs.CreateRun(txCtx, candidate)
		if err != nil {
			return err
		}
		if !ok {
			existing, err := uc.runs.FindByIdempotencyKey(txCtx, cmd.TenantID, *cmd.IdempotencyKey)
			if err != nil {
				return err
			}
			newRun = existing
			return nil
		}
		created = true
		if err := uc.steps.CloneStepRunsForRetry(txCtx, cmd.TenantID, candidate.ID, original.ID, nodes, cmd.RetryFailedStepsOnly); err != nil {
			return fmt.Errorf("clone step runs for retry: %w", err)
		}
		if err := uc.audit.Record(txCtx, domain.AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionRunRetryRequested,
			EntityType:  "workflow_run",
			EntityID:    &candidate.ID,
			Metadata:    mustJSON(map[string]any{"originalRunId": original.ID}),
		}); err != nil {
			return err
		}
		newRun = candidate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("retry run: %w", err)
	}

	if created {
		uc.publishEvent(ctx, domain.Event{
			Type:      domain.EventRunRetryRequested,
			TenantID:  cmd.TenantID,
			RunID:     &newRun.ID,
			Status:    domain.RunStatusPending,
			Timestamp: time.Now().UTC(),
		})
		if err := uc.queue.EnqueueRun(cmd.TenantID, newRun.ID); err != nil {
			uc.cfg.Logger.Warn("enqueue failed after retry, relying on stale-pending reclaim",
				slog.String("runID", newRun.ID.String()), slog.Any("error", err))
			return newRun, nil // recovered by ReclaimStalePendingRuns
		}
		uc.publishEvent(ctx, domain.Event{
			Type:      domain.EventRunQueued,
			TenantID:  cmd.TenantID,
			RunID:     &newRun.ID,
			Status:    domain.RunStatusPending,
			Timestamp: time.Now().UTC(),
		})
	}
	return newRun, nil
}

// CancelRun stops a running run. Steps already in flight finish, but no new
// steps are dispatched: the coordinator observes the canceled status on its
// next tick and stops.
func (uc *executionUseCase) CancelRun(ctx context.Context, tenantID, runID uuid.UUID) error {
	run, err := uc.runs.GetRun(ctx, tenantID, runID)
	if err != nil {
		return err
	}
	if run.Status != domain.RunStatusRunning {
		return ErrRunNotRunning
	}
	if !canTransitionRun(domain.RunStatusRunning, domain.RunStatusCanceled) {
		return ErrRunIllegalCancel
	}
	uc.publishEvent(ctx, domain.Event{
		Type:      domain.EventRunCancelRequested,
		TenantID:  tenantID,
		RunID:     &runID,
		Status:    domain.RunStatusRunning,
		Timestamp: time.Now().UTC(),
	})
	if err := uc.runs.UpdateRunStatus(ctx, tenantID, runID, domain.RunStatusCanceled, true); err != nil {
		return fmt.Errorf("cancel run: %w", err)
	}
	uc.publishEvent(ctx, domain.Event{
		Type:      domain.EventRunCancelled,
		TenantID:  tenantID,
		RunID:     &runID,
		Status:    domain.RunStatusCanceled,
		Timestamp: time.Now().UTC(),
	})
	if uc.audit != nil {
		if err := uc.audit.Record(ctx, domain.AuditEntry{
			TenantID:   tenantID,
			Action:     ActionRunCanceled,
			EntityType: "workflow_run",
			EntityID:   &runID,
			Metadata:   json.RawMessage(`{}`),
		}); err != nil {
			return fmt.Errorf("cancel run: audit: %w", err)
		}
	}
	return nil
}

func (uc *executionUseCase) publishEvent(ctx context.Context, ev domain.Event) {
	if uc.cfg.Metrics != nil {
		uc.cfg.Metrics.EventsPublished.WithLabelValues(ev.TenantID.String(), ev.Type).Inc()
	}
	if uc.cfg.Events == nil {
		return
	}
	if err := uc.cfg.Events.Publish(ctx, ev); err != nil {
		uc.cfg.Logger.Warn("eventstream: publish failed",
			slog.String("type", ev.Type),
			slog.String("tenantID", ev.TenantID.String()),
			slog.Any("error", err))
	}
}

func (uc *executionUseCase) GetRun(ctx context.Context, tenantID, runID uuid.UUID) (*domain.WorkflowRun, error) {
	return uc.runs.GetRun(ctx, tenantID, runID)
}

// ListRuns lists a workflow's runs, newest first, with normalized pagination.
// The parent workflow is checked first so a caller can distinguish "no runs
// yet" from "workflow does not exist" (404 WORKFLOW_NOT_FOUND).
func (uc *executionUseCase) ListRuns(ctx context.Context, q ListRunsQuery) (*PaginatedRuns, error) {
	if _, err := uc.workflows.GetWorkflow(ctx, q.TenantID, q.WorkflowID); err != nil {
		return nil, err
	}
	page, pageSize := normalizePagination(q.Page, q.PageSize)
	items, total, err := uc.runs.ListRuns(ctx, ListRunsFilter{
		TenantID:      q.TenantID,
		WorkflowID:    q.WorkflowID,
		Status:        q.Status,
		TriggerType:   q.TriggerType,
		CreatedAtFrom: q.CreatedAtFrom,
		CreatedAtTo:   q.CreatedAtTo,
		Page:          page,
		PageSize:      pageSize,
	})
	if err != nil {
		return nil, err
	}
	return &PaginatedRuns{Items: items, TotalItems: total, Page: page, PageSize: pageSize}, nil
}

// ListSteps lists a run's steps, parent-checked first.
func (uc *executionUseCase) ListSteps(ctx context.Context, tenantID, runID uuid.UUID) ([]*domain.StepRun, error) {
	if _, err := uc.runs.GetRun(ctx, tenantID, runID); err != nil {
		return nil, err
	}
	steps, err := uc.steps.ListStepRuns(ctx, tenantID, runID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.StepRun, 0, len(steps))
	for i := range steps {
		out = append(out, &steps[i])
	}
	return out, nil
}

// GetStep returns one step of a run.
func (uc *executionUseCase) GetStep(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error) {
	if _, err := uc.runs.GetRun(ctx, tenantID, runID); err != nil {
		return nil, err
	}
	return uc.steps.GetStepRun(ctx, tenantID, runID, stepRunID)
}

// ListLogs lists a run's execution logs, parent-checked first.
func (uc *executionUseCase) ListLogs(ctx context.Context, q ListLogsQuery) (*PaginatedLogs, error) {
	if _, err := uc.runs.GetRun(ctx, q.TenantID, q.RunID); err != nil {
		return nil, err
	}
	page, pageSize := normalizePagination(q.Page, q.PageSize)
	items, total, err := uc.logs.ListLogs(ctx, ListLogsFilter{
		TenantID:      q.TenantID,
		WorkflowRunID: q.RunID,
		Level:         q.Level,
		StepRunID:     q.StepRunID,
		Page:          page,
		PageSize:      pageSize,
	})
	if err != nil {
		return nil, err
	}
	return &PaginatedLogs{Items: items, TotalItems: total, Page: page, PageSize: pageSize}, nil
}

func normalizePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	} else if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

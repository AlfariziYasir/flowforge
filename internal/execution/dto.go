package execution

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/platform/eventstream"
	"flowforge/internal/platform/metrics"
)

// Usecase-level commands, queries, and results. Repository-level filter types
// (ListRunsFilter, ListLogsFilter) live in repository.go; the handler builds
// these and calls the usecase, which translates into repo filters.

// CreateRunCommand carries the inputs for starting a run.
type CreateRunCommand struct {
	TenantID       uuid.UUID
	WorkflowID     uuid.UUID
	ActorID        uuid.UUID
	TriggerType    string
	InputContext   json.RawMessage
	IdempotencyKey *string
}

// RetryRunCommand requests a retry of a finished run.
type RetryRunCommand struct {
	TenantID             uuid.UUID
	RunID                uuid.UUID
	ActorID              uuid.UUID
	RetryFailedStepsOnly bool
	IdempotencyKey       *string
}

// ListRunsQuery is the usecase-level run list request.
type ListRunsQuery struct {
	TenantID      uuid.UUID
	WorkflowID    uuid.UUID
	Status        string
	TriggerType   string
	CreatedAtFrom *time.Time
	CreatedAtTo   *time.Time
	Page          int
	PageSize      int
}

// PaginatedRuns wraps a page of runs with its total count.
type PaginatedRuns struct {
	Items      []*domain.WorkflowRun
	TotalItems int64
	Page       int
	PageSize   int
}

// ListLogsQuery is the usecase-level log list request.
type ListLogsQuery struct {
	TenantID  uuid.UUID
	RunID     uuid.UUID
	Level     string
	StepRunID string
	Page      int
	PageSize  int
}

// PaginatedLogs wraps a page of logs with its total count.
type PaginatedLogs struct {
	Items      []*domain.ExecutionLog
	TotalItems int64
	Page       int
	PageSize   int
}

// AnalyzeRunCommand requests AI failure analysis for a run. IncludeLogs and
// LogLimit are normalized by the handler (defaults true and 20).
type AnalyzeRunCommand struct {
	TenantID    uuid.UUID
	RunID       uuid.UUID
	IncludeLogs bool
	LogLimit    int
}

// AnalysisResult is the structured outcome of AnalyzeRun.
type AnalysisResult struct {
	RunID         uuid.UUID
	Diagnosis     string
	PossibleCause string
	SuggestedFix  string
	Confidence    float64
}

// ExecutionConfig groups the AI and telemetry knobs for the execution use case.
type ExecutionConfig struct {
	AIMaxRetries     int
	AIRequestTimeout time.Duration
	// Events publishes real-time monitoring events; nil-safe (D-3).
	Events eventstream.Publisher
	// Metrics may be nil; when set, usecase-level events increment EventsPublished.
	Metrics *metrics.Metrics
	// Logger is used for non-fatal warnings (e.g. a swallowed enqueue error).
	// Zero-value defaults to slog.Default() in the constructor.
	Logger *slog.Logger
}

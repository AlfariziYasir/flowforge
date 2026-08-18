package execution_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/platform/metrics"
)

func newTestUseCase(t *testing.T, runs *fakeRunRepo, steps *fakeStepRepo, logs *fakeLogReader,
	graph fakeGraph, enq *fakeEnqueuer, audit *fakeAuditRepo, ai *fakeAIProvider) (execution.ExecutionUseCase, *recordingTxRunner) {
	t.Helper()
	tx := &recordingTxRunner{}
	uc := execution.NewExecutionUseCase(
		fakeWorkflowReader{wf: &domain.Workflow{ID: uuid.New(), TenantID: uuid.New(), CurrentVersionID: ptr(uuid.New())}},
		runs, steps, logs, graph, enq, audit, tx, ai, nil, execution.ExecutionConfig{AIMaxRetries: 2, AIRequestTimeout: 0},
	)
	return uc, tx
}

func ptr[T any](v T) *T { return &v }

func testGraph() fakeGraph {
	return buildGraph([]nodeDef{
		{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		{"b", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, []edgeDef{{"a", "b", ""}})
}

func TestExecutionUseCase_CreateRun(t *testing.T) {
	t.Run("creates, seeds steps, audits, and enqueues inside a transaction", func(t *testing.T) {
		fr := &fakeRunRepo{}
		fs := newFakeStepRepo([]string{"a", "b"})
		enq := &fakeEnqueuer{}
		audit := &fakeAuditRepo{}
		uc, tx := newTestUseCase(t, fr, fs, nil, testGraph(), enq, audit, nil)

		run, err := uc.CreateRun(context.Background(), execution.CreateRunCommand{
			TenantID: uuid.New(), WorkflowID: uuid.New(), ActorID: uuid.New(),
		})
		require.NoError(t, err)
		assert.True(t, tx.called, "CreateRun must persist inside a transaction")
		assert.Equal(t, domain.RunStatusPending, run.Status)
		assert.Equal(t, 1, enq.calls, "run must be enqueued exactly once")
		assert.Equal(t, []string{execution.ActionRunCreated}, audit.actions())
	})

	t.Run("idempotency-key conflict returns the existing run without side effects", func(t *testing.T) {
		fr := &fakeRunRepo{skipCreate: true}
		existing := &domain.WorkflowRun{ID: uuid.New(), Status: domain.RunStatusPending}
		fr.idempotencyHit = existing
		fs := newFakeStepRepo([]string{"a", "b"})
		enq := &fakeEnqueuer{}
		audit := &fakeAuditRepo{}
		uc, _ := newTestUseCase(t, fr, fs, nil, testGraph(), enq, audit, nil)

		key := "dup-key"
		run, err := uc.CreateRun(context.Background(), execution.CreateRunCommand{
			TenantID: uuid.New(), WorkflowID: uuid.New(), IdempotencyKey: &key,
		})
		require.NoError(t, err)
		assert.Equal(t, existing.ID, run.ID, "the existing run is returned")
		assert.Zero(t, enq.calls, "duplicate must not enqueue")
		fs.mu.Lock()
		assert.Equal(t, engine.StepStatusPending, fs.steps["a"].Status, "duplicate must not seed steps")
		fs.mu.Unlock()
		assert.Empty(t, audit.actions(), "duplicate must not audit")
	})

	t.Run("a mid-transaction failure leaves the run un-enqueued", func(t *testing.T) {
		fr := &fakeRunRepo{}
		fs := newFakeStepRepo([]string{"a", "b"})
		fs.createStepErr = context.DeadlineExceeded
		enq := &fakeEnqueuer{}
		audit := &fakeAuditRepo{}
		uc, _ := newTestUseCase(t, fr, fs, nil, testGraph(), enq, audit, nil)

		_, err := uc.CreateRun(context.Background(), execution.CreateRunCommand{
			TenantID: uuid.New(), WorkflowID: uuid.New(),
		})
		require.Error(t, err)
		assert.Zero(t, enq.calls, "a rolled-back transaction must never leave an orphaned queue message")
	})
}

func TestExecutionUseCase_RetryRun(t *testing.T) {
	graph := testGraph()

	t.Run("eligibility table", func(t *testing.T) {
		cases := []struct {
			name   string
			status string
			want   error
		}{
			{"succeeded is already completed", domain.RunStatusSucceeded, execution.ErrRunAlreadyCompleted},
			{"pending is already running", domain.RunStatusPending, execution.ErrRunAlreadyRunning},
			{"running is already running", domain.RunStatusRunning, execution.ErrRunAlreadyRunning},
			{"failed may be retried", domain.RunStatusFailed, nil},
			{"canceled may be retried", domain.RunStatusCanceled, nil},
			{"timed_out may be retried", domain.RunStatusTimedOut, nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fr := &fakeRunRepo{run: &domain.WorkflowRun{
					ID: uuid.New(), TenantID: uuid.New(), Status: tc.status,
				}}
				enq := &fakeEnqueuer{}
				uc, _ := newTestUseCase(t, fr, newFakeStepRepo(nil), nil, graph, enq, &fakeAuditRepo{}, nil)

				_, err := uc.RetryRun(context.Background(), execution.RetryRunCommand{
					TenantID: fr.run.TenantID, RunID: fr.run.ID,
				})
				if tc.want != nil {
					require.ErrorIs(t, err, tc.want)
					assert.Zero(t, enq.calls)
				} else {
					require.NoError(t, err)
					assert.Equal(t, 1, enq.calls)
				}
			})
		}
	})

	t.Run("reuses the original run's version, not the workflow's current one", func(t *testing.T) {
		origVer := uuid.New()
		fr := &fakeRunRepo{run: &domain.WorkflowRun{
			ID: uuid.New(), TenantID: uuid.New(), WorkflowID: uuid.New(),
			WorkflowVersionID: origVer, Status: domain.RunStatusFailed,
		}}
		fs := newFakeStepRepo(nil)
		uc, _ := newTestUseCase(t, fr, fs, nil, graph, &fakeEnqueuer{}, &fakeAuditRepo{}, nil)

		run, err := uc.RetryRun(context.Background(), execution.RetryRunCommand{
			TenantID: fr.run.TenantID, RunID: fr.run.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, origVer, run.WorkflowVersionID, "retry must replay the original version")
		assert.NotNil(t, run.RetriedFromRunID)
		assert.Equal(t, fr.run.ID, *run.RetriedFromRunID)
	})

	t.Run("retryFailedStepsOnly is forwarded to the clone", func(t *testing.T) {
		origID := uuid.New()
		fr := &fakeRunRepo{run: &domain.WorkflowRun{
			ID: origID, TenantID: uuid.New(), WorkflowID: uuid.New(),
			WorkflowVersionID: uuid.New(), Status: domain.RunStatusFailed,
		}}
		fs := newFakeStepRepo(nil)
		uc, _ := newTestUseCase(t, fr, fs, nil, graph, &fakeEnqueuer{}, &fakeAuditRepo{}, nil)

		_, err := uc.RetryRun(context.Background(), execution.RetryRunCommand{
			TenantID: fr.run.TenantID, RunID: origID, RetryFailedStepsOnly: true,
		})
		require.NoError(t, err)
		fs.mu.Lock()
		defer fs.mu.Unlock()
		require.Len(t, fs.cloneCalls, 1)
		assert.True(t, fs.cloneCalls[0].onlyFailed)
		assert.Equal(t, origID, fs.cloneCalls[0].originalRunID)
	})
}

func TestExecutionUseCase_ParentCheckedChildren(t *testing.T) {
	t.Run("ListSteps propagates a missing parent before touching the step repo", func(t *testing.T) {
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New()}}
		fs := newFakeStepRepo(nil)
		uc, _ := newTestUseCase(t, fr, fs, nil, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, nil)
		// Force the parent check to fail: the fake's GetRun returns a run, so use
		// a run repo that returns not-found by having the run nil.
		fr.run = nil
		_, err := uc.ListSteps(context.Background(), uuid.New(), uuid.New())
		require.ErrorIs(t, err, execution.ErrRunNotFound)
		fs.mu.Lock()
		assert.Len(t, fs.steps, 0, "child repo must not be consulted when the parent is missing")
		fs.mu.Unlock()
	})

	t.Run("ListLogs propagates a missing parent", func(t *testing.T) {
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New()}}
		logs := &fakeLogReader{items: []*domain.ExecutionLog{{Message: "x"}}}
		uc, _ := newTestUseCase(t, fr, newFakeStepRepo(nil), logs, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, nil)
		fr.run = nil
		_, err := uc.ListLogs(context.Background(), execution.ListLogsQuery{TenantID: uuid.New(), RunID: uuid.New()})
		require.ErrorIs(t, err, execution.ErrRunNotFound)
		assert.Zero(t, logs.calls, "child repo must not be consulted when the parent is missing")
	})
}

func TestExecutionUseCase_AnalyzeRun(t *testing.T) {
	secret := "super-secret-value-planted"
	stepPayload := []byte(`{"statusCode":200,"secret":"` + secret + `"}`)
	validJSON := `{"diagnosis":"d","possibleCause":"p","suggestedFix":"f","confidence":0.8}`

	t.Run("planted secret never reaches the provider", func(t *testing.T) {
		fs := newFakeStepRepo([]string{"a"})
		fs.steps["a"].Status = engine.StepStatusFailed
		fs.steps["a"].OutputPayload = stepPayload
		ai := &fakeAIProvider{response: validJSON}
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusFailed}}
		uc, _ := newTestUseCase(t, fr, fs, &fakeLogReader{}, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, ai)

		res, err := uc.AnalyzeRun(context.Background(), execution.AnalyzeRunCommand{
			TenantID: fr.run.TenantID, RunID: fr.run.ID, IncludeLogs: true, LogLimit: 20,
		})
		require.NoError(t, err)
		assert.Equal(t, "d", res.Diagnosis)
		assert.NotContains(t, ai.lastUserPrompt, secret, "the redacted prompt must never contain the planted secret")
		assert.Contains(t, ai.lastUserPrompt, "***REDACTED***")
	})

	t.Run("any run status is eligible", func(t *testing.T) {
		for _, st := range []string{domain.RunStatusRunning, domain.RunStatusSucceeded, domain.RunStatusCanceled} {
			fs := newFakeStepRepo(nil)
			fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: st}}
			ai := &fakeAIProvider{response: validJSON}
			uc, _ := newTestUseCase(t, fr, fs, &fakeLogReader{}, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, ai)
			res, err := uc.AnalyzeRun(context.Background(), execution.AnalyzeRunCommand{
				TenantID: fr.run.TenantID, RunID: fr.run.ID, IncludeLogs: true,
			})
			require.NoError(t, err, "status %q must be analyzable", st)
			assert.NotEmpty(t, res.Diagnosis)
		}
	})

	t.Run("malformed JSON is retried then rejected after max retries", func(t *testing.T) {
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusFailed}}
		ai := &fakeAIProvider{response: "not json at all"}
		uc, _ := newTestUseCase(t, fr, newFakeStepRepo(nil), &fakeLogReader{}, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, ai)
		_, err := uc.AnalyzeRun(context.Background(), execution.AnalyzeRunCommand{
			TenantID: fr.run.TenantID, RunID: fr.run.ID, IncludeLogs: false,
		})
		require.ErrorIs(t, err, execution.ErrAIInvalidResponse)
	})

	t.Run("confidence outside [0,1] takes the same retry-and-fail path", func(t *testing.T) {
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusFailed}}
		ai := &fakeAIProvider{response: `{"diagnosis":"d","possibleCause":"p","suggestedFix":"f","confidence":1.5}`}
		uc, _ := newTestUseCase(t, fr, newFakeStepRepo(nil), &fakeLogReader{}, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, ai)
		_, err := uc.AnalyzeRun(context.Background(), execution.AnalyzeRunCommand{
			TenantID: fr.run.TenantID, RunID: fr.run.ID, IncludeLogs: false,
		})
		require.ErrorIs(t, err, execution.ErrAIInvalidResponse)
	})

	t.Run("provider failure wraps as ErrAIGenerationFailed", func(t *testing.T) {
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusFailed}}
		ai := &fakeAIProvider{err: context.DeadlineExceeded}
		uc, _ := newTestUseCase(t, fr, newFakeStepRepo(nil), &fakeLogReader{}, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, ai)
		_, err := uc.AnalyzeRun(context.Background(), execution.AnalyzeRunCommand{
			TenantID: fr.run.TenantID, RunID: fr.run.ID, IncludeLogs: false,
		})
		require.ErrorIs(t, err, execution.ErrAIGenerationFailed)
	})

	t.Run("successful analysis is audited", func(t *testing.T) {
		fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusFailed}}
		ai := &fakeAIProvider{response: validJSON}
		audit := &fakeAuditRepo{}
		uc, _ := newTestUseCase(t, fr, newFakeStepRepo(nil), &fakeLogReader{}, fakeGraph{}, &fakeEnqueuer{}, audit, ai)
		_, err := uc.AnalyzeRun(context.Background(), execution.AnalyzeRunCommand{
			TenantID: fr.run.TenantID, RunID: fr.run.ID, IncludeLogs: false,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{execution.ActionRunAnalysisGenerated}, audit.actions())
	})
}

// Z-3: ListRuns parent-checks the workflow before consulting the run repo.
func TestExecutionUseCase_ListRunsParentCheck(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()

	fr := &fakeRunRepo{}
	fs := newFakeStepRepo(nil)
	uc := execution.NewExecutionUseCase(
		fakeWorkflowReader{err: execution.ErrRunNotFound}, // workflow reader errors
		fr, fs, nil, fakeGraph{}, &fakeEnqueuer{}, &fakeAuditRepo{}, &recordingTxRunner{}, nil, nil,
		execution.ExecutionConfig{},
	)

	_, err := uc.ListRuns(context.Background(), execution.ListRunsQuery{TenantID: tenantID, WorkflowID: wfID})
	require.ErrorIs(t, err, execution.ErrRunNotFound)
	fr.mu.Lock()
	assert.Zero(t, fr.listRunsCalls, "the run repo must not be consulted when the parent workflow is missing")
	fr.mu.Unlock()
}

// AD-2: ExecutionUseCase records EventsPublished metric on event publication.
func TestExecutionUseCase_PublishEvent_RecordsMetric(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()
	verID := uuid.New()

	fr := &fakeRunRepo{}
	fs := newFakeStepRepo([]string{"a", "b"})
	enq := &fakeEnqueuer{}
	audit := &fakeAuditRepo{}
	tx := &recordingTxRunner{}
	events := &fakeEvents{}

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	uc := execution.NewExecutionUseCase(
		fakeWorkflowReader{wf: &domain.Workflow{ID: wfID, TenantID: tenantID, CurrentVersionID: &verID}},
		fr, fs, nil, testGraph(), enq, audit, tx, nil, nil,
		execution.ExecutionConfig{
			AIMaxRetries:     2,
			AIRequestTimeout: 0,
			Events:           events,
			Metrics:          m,
		},
	)

	_, err := uc.CreateRun(context.Background(), execution.CreateRunCommand{
		TenantID:   tenantID,
		WorkflowID: wfID,
		ActorID:    uuid.New(),
	})
	require.NoError(t, err)

	// EventsPublished counter should increment for both workflow.run.created and workflow.run.queued
	valCreated := testutil.ToFloat64(m.EventsPublished.WithLabelValues(tenantID.String(), domain.EventRunCreated))
	assert.Equal(t, 1.0, valCreated, "EventsPublished must increment for workflow.run.created")

	valQueued := testutil.ToFloat64(m.EventsPublished.WithLabelValues(tenantID.String(), domain.EventRunQueued))
	assert.Equal(t, 1.0, valQueued, "EventsPublished must increment for workflow.run.queued")
}

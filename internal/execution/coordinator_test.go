package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/execution/executor"
)

// ---------- fakes ----------

type recordedWrite struct {
	from, to string
}

type fakeStepRepo struct {
	mu            sync.Mutex
	steps         map[string]*domain.StepRun // by nodeKey
	writes        []recordedWrite
	logged        bool
	claimErr      error
	createStepErr error
	cloneCalls    []cloneCall
}

type cloneCall struct {
	originalRunID uuid.UUID
	onlyFailed    bool
}

func newFakeStepRepo(keys []string) *fakeStepRepo {
	f := &fakeStepRepo{steps: map[string]*domain.StepRun{}}
	for _, k := range keys {
		f.steps[k] = &domain.StepRun{
			ID:            uuid.New(),
			TenantID:      uuid.New(),
			WorkflowRunID: uuid.New(),
			NodeKey:       k,
			Status:        engine.StepStatusPending,
		}
	}
	return f
}

func (f *fakeStepRepo) set(key, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.steps[key]; s != nil {
		s.Status = status
	}
}

func (f *fakeStepRepo) CreateStepRuns(ctx context.Context, tenantID, runID uuid.UUID, nodes []domain.WorkflowNode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createStepErr
}

func (f *fakeStepRepo) MarkStepsReady(ctx context.Context, tenantID, runID uuid.UUID, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		if s := f.steps[k]; s != nil && s.Status == engine.StepStatusPending {
			f.writes = append(f.writes, recordedWrite{engine.StepStatusPending, engine.StepStatusReady})
			s.Status = engine.StepStatusReady
		}
	}
	return nil
}

func (f *fakeStepRepo) MarkStepsSkipped(ctx context.Context, tenantID, runID uuid.UUID, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		if s := f.steps[k]; s != nil && s.Status == engine.StepStatusPending {
			f.writes = append(f.writes, recordedWrite{engine.StepStatusPending, engine.StepStatusSkipped})
			s.Status = engine.StepStatusSkipped
		}
	}
	return nil
}

func (f *fakeStepRepo) ClaimReadySteps(ctx context.Context, tenantID, runID uuid.UUID, limit int) ([]domain.StepRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	var keys []string
	for k := range f.steps {
		st := f.steps[k].Status
		if st == engine.StepStatusReady || st == engine.StepStatusRetrying {
			keys = append(keys, k)
		}
	}
	// deterministic order
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]domain.StepRun, 0, len(keys))
	for _, k := range keys {
		s := f.steps[k]
		f.writes = append(f.writes, recordedWrite{s.Status, engine.StepStatusRunning})
		s.Status = engine.StepStatusRunning
		now := time.Now()
		s.StartedAt = &now
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeStepRepo) UpdateStepStatus(ctx context.Context, tenantID, runID uuid.UUID, key, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.steps[key]; s != nil {
		f.writes = append(f.writes, recordedWrite{s.Status, status})
		s.Status = status
	}
	return nil
}

func (f *fakeStepRepo) UpdateStepResult(ctx context.Context, tenantID, runID uuid.UUID, key string, u execution.StepUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.steps[key]
	if s == nil {
		return nil
	}
	f.writes = append(f.writes, recordedWrite{s.Status, u.Status})
	s.Status = u.Status
	s.AttemptCount = u.AttemptCount
	s.OutputPayload = u.Output
	s.ErrorPayload = u.ErrorPayload
	s.FinishedAt = u.FinishedAt
	return nil
}

func (f *fakeStepRepo) ListStepRuns(ctx context.Context, tenantID, runID uuid.UUID) ([]domain.StepRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.StepRun, 0, len(f.steps))
	for _, s := range f.steps {
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeStepRepo) GetStepRun(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.steps {
		if s.ID == stepRunID {
			cp := *s
			return &cp, nil
		}
	}
	return nil, execution.ErrStepNotFound
}

func (f *fakeStepRepo) CloneStepRunsForRetry(ctx context.Context, tenantID, newRunID, originalRunID uuid.UUID, nodes []domain.WorkflowNode, onlyFailed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cloneCalls = append(f.cloneCalls, cloneCall{originalRunID: originalRunID, onlyFailed: onlyFailed})
	return nil
}

type fakeRunRepo struct {
	mu             sync.Mutex
	run            *domain.WorkflowRun
	claimNil       bool
	statuses       []string
	getCalls       int
	extendErr      error
	createErr      error
	createCalls    int
	skipCreate     bool
	idempotencyHit *domain.WorkflowRun
	idempotencyErr error
	listRunsResult []*domain.WorkflowRun
	listRunsTotal  int64
	listRunsCalls  int
	staleResult    []execution.ReclaimedRun
}

func (f *fakeRunRepo) CreateRun(ctx context.Context, run *domain.WorkflowRun) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return false, f.createErr
	}
	return !f.skipCreate, nil
}
func (f *fakeRunRepo) GetRun(ctx context.Context, tenantID, runID uuid.UUID) (*domain.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.run == nil {
		return nil, execution.ErrRunNotFound
	}
	cp := *f.run
	return &cp, nil
}
func (f *fakeRunRepo) FindByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idempotencyErr != nil {
		return nil, f.idempotencyErr
	}
	if f.idempotencyHit != nil {
		return f.idempotencyHit, nil
	}
	return nil, execution.ErrRunNotFound
}
func (f *fakeRunRepo) ClaimRun(ctx context.Context, tenantID, runID uuid.UUID, workerID string, lease time.Duration) (*domain.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimNil {
		return nil, nil
	}
	f.run.Status = domain.RunStatusRunning
	f.run.ClaimedBy = &workerID
	cp := *f.run
	return &cp, nil
}
func (f *fakeRunRepo) UpdateRunStatus(ctx context.Context, tenantID, runID uuid.UUID, status string, finished bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	f.run.Status = status
	return nil
}
func (f *fakeRunRepo) ExtendLease(ctx context.Context, tenantID, runID uuid.UUID, workerID string, lease time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.extendErr
}
func (f *fakeRunRepo) ReclaimExpiredLeases(ctx context.Context) ([]execution.ReclaimedRun, error) {
	return nil, nil
}
func (f *fakeRunRepo) ReclaimStalePendingRuns(ctx context.Context, olderThan time.Duration) ([]execution.ReclaimedRun, error) {
	return f.staleResult, nil
}
func (f *fakeRunRepo) ListRuns(ctx context.Context, flt execution.ListRunsFilter) ([]*domain.WorkflowRun, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listRunsCalls++
	return f.listRunsResult, f.listRunsTotal, nil
}

type fakeGraph struct {
	nodes []domain.WorkflowNode
	edges []domain.WorkflowEdge
}

func (f fakeGraph) LoadGraph(ctx context.Context, tenantID, versionID uuid.UUID) ([]domain.WorkflowNode, []domain.WorkflowEdge, error) {
	return f.nodes, f.edges, nil
}

type fakeLogs struct {
	mu      sync.Mutex
	calls   int
	entries []*domain.ExecutionLog
	err     error
}

func (f *fakeLogs) Append(ctx context.Context, entry *domain.ExecutionLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.entries = append(f.entries, entry)
	return f.err
}

func (f *fakeLogs) getEntries() []*domain.ExecutionLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.ExecutionLog, len(f.entries))
	copy(out, f.entries)
	return out
}

type fakeEvents struct {
	mu     sync.Mutex
	events []domain.Event
	err    error
}

func (f *fakeEvents) Publish(ctx context.Context, ev domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return f.err
}

func (f *fakeEvents) getEvents() []domain.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Event, len(f.events))
	copy(out, f.events)
	return out
}

// recordingTxRunner runs fn inline and records that it was invoked.
type recordingTxRunner struct {
	called bool
}

func (r *recordingTxRunner) ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	r.called = true
	return fn(ctx)
}

// fakeAuditRepo captures every entry so tests can assert what was audited.
type fakeAuditRepo struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (f *fakeAuditRepo) Record(ctx context.Context, e domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAuditRepo) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e.Action)
	}
	return out
}

// fakeAIProvider returns a canned response (or error) and records the prompts
// so redaction tests can assert a planted secret never reached it.
type fakeAIProvider struct {
	response         string
	err              error
	lastSystemPrompt string
	lastUserPrompt   string
}

func (f *fakeAIProvider) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	f.lastSystemPrompt = systemPrompt
	f.lastUserPrompt = userPrompt
	if f.err != nil {
		return "", f.err
	}
	return f.response, nil
}

// fakeLogReader returns canned log pages and counts calls.
type fakeLogReader struct {
	items []*domain.ExecutionLog
	total int64
	err   error
	calls int
}

func (f *fakeLogReader) ListLogs(ctx context.Context, flt execution.ListLogsFilter) ([]*domain.ExecutionLog, int64, error) {
	f.calls++
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.items, f.total, nil
}

// fakeWorkflowReader returns a canned workflow (its current version pointer).
type fakeWorkflowReader struct {
	wf  *domain.Workflow
	err error
}

func (f fakeWorkflowReader) GetWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.Workflow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.wf, nil
}

// fakeEnqueuer counts enqueue calls and can fail.
type fakeEnqueuer struct {
	calls int
	err   error
}

func (f *fakeEnqueuer) EnqueueRun(tenantID, runID uuid.UUID) error {
	f.calls++
	return f.err
}

func mustNode(nodeType string, cfg map[string]any) domain.WorkflowNode {
	raw, _ := json.Marshal(cfg)
	return domain.WorkflowNode{
		ID:       uuid.New(),
		NodeKey:  nodeType,
		NodeType: nodeType,
		Config:   raw,
	}
}

func wfNode(key, nodeType string, cfg map[string]any) domain.WorkflowNode {
	raw, _ := json.Marshal(cfg)
	return domain.WorkflowNode{ID: uuid.New(), NodeKey: key, NodeType: nodeType, Config: raw}
}

type nodeDef struct {
	key, nodeType string
	cfg           map[string]any
}

type edgeDef struct {
	from, to, branch string
}

// buildGraph wires nodes and edges with consistent UUIDs so FromPersisted can
// resolve keys, and (optionally) a tenant/version for scoping.
func buildGraph(nodes []nodeDef, edges []edgeDef) fakeGraph {
	idByKey := map[string]uuid.UUID{}
	var ns []domain.WorkflowNode
	for _, d := range nodes {
		n := wfNode(d.key, d.nodeType, d.cfg)
		idByKey[d.key] = n.ID
		ns = append(ns, n)
	}
	var es []domain.WorkflowEdge
	for _, e := range edges {
		branch := e.branch
		if branch == "" {
			branch = "default"
		}
		es = append(es, domain.WorkflowEdge{
			ID:         uuid.New(),
			FromNodeID: idByKey[e.from],
			ToNodeID:   idByKey[e.to],
			Branch:     branch,
		})
	}
	return fakeGraph{nodes: ns, edges: es}
}

// graphFixture builds the coordinator's dependencies. nodes/edges come from the
// caller; the run input context feeds engine scope.
func coord(t *testing.T, graph fakeGraph, stepKeys []string, runCtx map[string]any,
	retry engine.RetryPolicy, lease ...time.Duration) (*execution.Coordinator, *fakeRunRepo, *fakeStepRepo) {
	t.Helper()

	fr := &fakeRunRepo{run: &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          uuid.New(),
		WorkflowID:        uuid.New(),
		WorkflowVersionID: uuid.New(),
		Status:            domain.RunStatusRunning,
	}}
	if runCtx != nil {
		fr.run.InputContext, _ = json.Marshal(runCtx)
	}
	fs := newFakeStepRepo(stepKeys)
	logs := &fakeLogs{}

	c := execution.NewCoordinator(fr, fs, logs, graph, executor.NewRegistry(nil, 1<<20, nil), execution.CoordinatorConfig{
		Concurrency: 4,
		Lease:       60 * time.Second,
		Retry:       retry,
		Timeout:     engine.DefaultTimeoutPolicy(),
		WorkerID:    "test",
		Logger:      slog.New(slog.DiscardHandler),
	})
	if len(lease) > 0 {
		c = execution.NewCoordinator(fr, fs, logs, graph, executor.NewRegistry(nil, 1<<20, nil), execution.CoordinatorConfig{
			Concurrency:   4,
			Lease:         lease[0],
			Retry:         retry,
			Timeout:       engine.DefaultTimeoutPolicy(),
			WorkerID:      "test",
			Logger:        slog.New(slog.DiscardHandler),
			TickIdleDelay: time.Second,
		})
	}
	return c, fr, fs
}

type fakeWaitTokenStore struct {
	mu     sync.Mutex
	tokens map[string]domain.StepWaitToken
}

func (f *fakeWaitTokenStore) CreateToken(ctx context.Context, token domain.StepWaitToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokens == nil {
		f.tokens = make(map[string]domain.StepWaitToken)
	}
	f.tokens[token.CorrelationKey] = token
	return nil
}

func (f *fakeWaitTokenStore) CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error) {
	return 0, nil
}

func coordWithLogs(t *testing.T, graph fakeGraph, stepKeys []string, runCtx map[string]any,
	retry engine.RetryPolicy, logs *fakeLogs) (*execution.Coordinator, *fakeRunRepo, *fakeStepRepo) {
	t.Helper()

	fr := &fakeRunRepo{run: &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          uuid.New(),
		WorkflowID:        uuid.New(),
		WorkflowVersionID: uuid.New(),
		Status:            domain.RunStatusRunning,
	}}
	if runCtx != nil {
		fr.run.InputContext, _ = json.Marshal(runCtx)
	}
	fs := newFakeStepRepo(stepKeys)

	c := execution.NewCoordinator(fr, fs, logs, graph, executor.NewRegistry(nil, 1<<20, nil), execution.CoordinatorConfig{
		Concurrency: 4,
		Lease:       60 * time.Second,
		Retry:       retry,
		Timeout:     engine.DefaultTimeoutPolicy(),
		WorkerID:    "test",
		Logger:      slog.New(slog.DiscardHandler),
		WaitTokens:  &fakeWaitTokenStore{},
	})
	return c, fr, fs
}

func coordWithEvents(t *testing.T, graph fakeGraph, stepKeys []string, runCtx map[string]any,
	retry engine.RetryPolicy, events *fakeEvents) (*execution.Coordinator, *fakeRunRepo, *fakeStepRepo) {
	t.Helper()

	fr := &fakeRunRepo{run: &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          uuid.New(),
		WorkflowID:        uuid.New(),
		WorkflowVersionID: uuid.New(),
		Status:            domain.RunStatusRunning,
	}}
	if runCtx != nil {
		fr.run.InputContext, _ = json.Marshal(runCtx)
	}
	fs := newFakeStepRepo(stepKeys)

	c := execution.NewCoordinator(fr, fs, &fakeLogs{}, graph, executor.NewRegistry(nil, 1<<20, nil), execution.CoordinatorConfig{
		Concurrency: 4,
		Lease:       60 * time.Second,
		Retry:       retry,
		Timeout:     engine.DefaultTimeoutPolicy(),
		WorkerID:    "test",
		Logger:      slog.New(slog.DiscardHandler),
		WaitTokens:  &fakeWaitTokenStore{},
		Events:      events,
	})
	return c, fr, fs
}

func defaultRetry() engine.RetryPolicy {
	return engine.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond}
}

// Q-1: a linear graph is driven to succeeded, each step in order.
func TestCoordinator_LinearGraphSucceeds(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		{"b", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, []edgeDef{{"a", "b", ""}})
	c, fr, fs := coord(t, graph, []string{"a", "b"}, nil, defaultRetry())

	err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
	require.NoError(t, err)

	assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["a"].Status)
	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["b"].Status)
}

// Q-2: a CONDITION false skips the false branch transitively.
func TestCoordinator_ConditionSkipsBranch(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"cond", domain.NodeTypeCondition, map[string]any{"expression": "trigger.x > 5"}},
		{"yes", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		{"no", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, []edgeDef{
		{"cond", "yes", "true"},
		{"cond", "no", "false"},
	})
	c, fr, fs := coord(t, graph, []string{"cond", "yes", "no"}, map[string]any{"x": 3}, defaultRetry())

	err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
	require.NoError(t, err)

	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["cond"].Status)
	assert.Equal(t, engine.StepStatusSkipped, fs.steps["yes"].Status, "untaken true branch is skipped")
	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["no"].Status)
	assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
}

// Q-3: a failing step with attempts remaining retries; exhausted attempts fail
// the run.
func TestCoordinator_RetriesThenFails(t *testing.T) {
	// DELAY with a negative duration always errors → deterministic failure.
	graph := buildGraph([]nodeDef{
		{"bad", domain.NodeTypeDelay, map[string]any{"seconds": -1}},
	}, nil)
	c, fr, fs := coord(t, graph, []string{"bad"}, nil, engine.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})

	err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
	require.NoError(t, err)

	assert.Equal(t, engine.StepStatusFailed, fs.steps["bad"].Status)
	assert.Equal(t, 2, fs.steps["bad"].AttemptCount, "both attempts must be spent")
	assert.Equal(t, []string{domain.RunStatusFailed}, fr.statuses)
}

// Q-4: every status write the coordinator emits is a legal transition.
func TestCoordinator_NoIllegalTransitionsWritten(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		{"b", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, []edgeDef{{"a", "b", ""}})
	c, fr, fs := coord(t, graph, []string{"a", "b"}, nil, defaultRetry())

	require.NoError(t, c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID))

	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, w := range fs.writes {
		assert.True(t, engine.CanTransition(w.from, w.to), "illegal step transition %s -> %s", w.from, w.to)
	}
	assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
}

// Q-5: a cancelled context stops dispatch and HandleRun returns promptly with
// every goroutine joined (the heartbeat WaitGroup is awaited before return).
func TestCoordinator_CancelledContextStops(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"slow", domain.NodeTypeDelay, map[string]any{"seconds": 60}},
	}, nil)
	c, fr, _ := coord(t, graph, []string{"slow"}, nil, defaultRetry())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = c.HandleRun(ctx, fr.run.TenantID, fr.run.ID)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("HandleRun did not return promptly after cancellation")
	}
}

// Q-6: the scope is rebuilt from persisted step outputs every tick, never
// carried in memory.
func TestCoordinator_ScopeRebuiltFromPersistedOutputs(t *testing.T) {
	// a(TRANSFORM) -> cond reads steps.a.output.value; true branch runs.
	graph := buildGraph([]nodeDef{
		{"a", domain.NodeTypeTransform, map[string]any{"expression": "\"hello\""}},
		{"cond", domain.NodeTypeCondition, map[string]any{"expression": "steps.a.output.value == \"hello\""}},
		{"yes", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		{"no", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, []edgeDef{
		{"a", "cond", ""},
		{"cond", "yes", "true"},
		{"cond", "no", "false"},
	})
	c, fr, fs := coord(t, graph, []string{"a", "cond", "yes", "no"}, nil, defaultRetry())

	require.NoError(t, c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID))

	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["a"].Status)
	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["cond"].Status)
	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["yes"].Status, "true branch evaluated from persisted output")
	assert.Equal(t, engine.StepStatusSkipped, fs.steps["no"].Status)
	assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
}

// Q-7: a zero-row run claim (duplicate delivery) is a no-op with no error.
func TestCoordinator_DuplicateDeliveryNoOp(t *testing.T) {
	graph := buildGraph([]nodeDef{{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}}}, nil)
	c, fr, fs := coord(t, graph, []string{"a"}, nil, defaultRetry())
	fr.claimNil = true

	err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
	require.NoError(t, err)
	assert.Empty(t, fr.statuses, "no run status write on duplicate delivery")
	assert.Equal(t, engine.StepStatusPending, fs.steps["a"].Status, "no step was touched")
}

// Y-1: when the heartbeat learns the lease was lost (another worker owns the
// run now), HandleRun must stop promptly and dispatch nothing further.
func TestHandleRun_StopsWhenLeaseLost(t *testing.T) {
	// A 60s step would otherwise keep HandleRun busy; the lease-loss cancel must
	// end it fast. Lease=1s → heartbeat interval 500ms.
	graph := buildGraph([]nodeDef{
		{"slow", domain.NodeTypeDelay, map[string]any{"seconds": 60}},
	}, nil)
	c, fr, fs := coord(t, graph, []string{"slow"}, nil, defaultRetry(), time.Second)
	fr.extendErr = execution.ErrLeaseLost

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.HandleRun(ctx, fr.run.TenantID, fr.run.ID) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("HandleRun did not stop after the lease was lost")
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	assert.Equal(t, engine.StepStatusRunning, fs.steps["slow"].Status,
		"a step interrupted by lease loss must be left 'running' for the reaper, never completed or re-dispatched")
}

// Y-2: a run whose only non-terminal step is 'running' (nothing claimable) must
// not make HandleRun spin: ticks are bounded by the idle brake.
func TestHandleRun_DoesNotSpinOnOrphanedStep(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"orphan", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, nil)
	c, fr, fs := coord(t, graph, []string{"orphan"}, nil, defaultRetry())
	fs.set("orphan", engine.StepStatusRunning) // stuck running, nothing claimable

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.HandleRun(ctx, fr.run.TenantID, fr.run.ID)

	fr.mu.Lock()
	getCalls := fr.getCalls
	fr.mu.Unlock()
	assert.LessOrEqual(t, getCalls, 10, "HandleRun must idle, not spin: GetRun called %d times", getCalls)
}

// fakeWaitTokens satisfies executor.WaitTokenStore for coordinator tests.
type fakeWaitTokens struct {
	created []domain.StepWaitToken
}

func (f *fakeWaitTokens) CreateToken(ctx context.Context, token domain.StepWaitToken) error {
	f.created = append(f.created, token)
	return nil
}

func (f *fakeWaitTokens) CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error) {
	return 0, nil
}

// Phase 7: an EVENT_WAIT step parks both the step and the run in 'waiting' and
// HandleRun returns without holding a goroutine.
func TestCoordinator_EventWaitParksRun(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"wait", domain.NodeTypeEventWait, map[string]any{"correlationKey": "{{trigger.orderId}}"}},
	}, nil)
	fr := &fakeRunRepo{run: &domain.WorkflowRun{
		ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusRunning,
	}}
	fr.run.InputContext, _ = json.Marshal(map[string]any{"orderId": "ORD-1"})
	fs := newFakeStepRepo([]string{"wait"})
	store := &fakeWaitTokens{}

	c := execution.NewCoordinator(fr, fs, &fakeLogs{}, graph, executor.NewRegistry(nil, 1<<20, nil),
		execution.CoordinatorConfig{
			Concurrency:         4,
			Lease:               60 * time.Second,
			Retry:               engine.DefaultRetryPolicy(),
			Timeout:             engine.DefaultTimeoutPolicy(),
			WorkerID:            "test",
			Logger:              slog.New(slog.DiscardHandler),
			WaitTokens:          store,
			MaxWaitPerTenant:    5,
			DefaultWaitDuration: time.Hour,
			MaxWaitDuration:     24 * time.Hour,
		})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.HandleRun(ctx, fr.run.TenantID, fr.run.ID) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("HandleRun must return promptly when the run parks on a wait token")
	}

	assert.Equal(t, domain.RunStatusWaiting, fr.run.Status, "the run must park in 'waiting'")
	fs.mu.Lock()
	assert.Equal(t, engine.StepStatusWaiting, fs.steps["wait"].Status, "the step must park in 'waiting'")
	fs.mu.Unlock()
}

// fakeWaitTokenRepo satisfies execution.WaitTokenRepository for usecase tests.
type fakeWaitTokenRepo struct {
	mu      sync.Mutex
	byKey   map[string]domain.StepWaitToken
	secret  string
	orphans []domain.OrphanEvent
}

func newFakeWaitTokenRepo() *fakeWaitTokenRepo {
	return &fakeWaitTokenRepo{byKey: map[string]domain.StepWaitToken{}}
}

func (f *fakeWaitTokenRepo) CreateToken(ctx context.Context, token *domain.StepWaitToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byKey[token.CorrelationKey] = *token
	return nil
}
func (f *fakeWaitTokenRepo) CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error) {
	return 0, nil
}
func (f *fakeWaitTokenRepo) FindTokenByCorrelationKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.StepWaitToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.byKey[key]
	if !ok {
		return nil, execution.ErrTokenNotFound
	}
	return &t, nil
}
func (f *fakeWaitTokenRepo) ConsumeTokenIfUnconsumed(ctx context.Context, tokenID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.byKey {
		if t.ID == tokenID {
			if t.ConsumedAt != nil {
				return false, nil
			}
			now := time.Now()
			t.ConsumedAt = &now
			f.byKey[k] = t
			return true, nil
		}
	}
	return false, execution.ErrTokenNotFound
}
func (f *fakeWaitTokenRepo) MarkWaitingStepSucceeded(ctx context.Context, tenantID, stepRunID uuid.UUID, output []byte) error {
	return nil
}
func (f *fakeWaitTokenRepo) MarkWaitingStepFailed(ctx context.Context, tenantID, stepRunID uuid.UUID) error {
	return nil
}
func (f *fakeWaitTokenRepo) MarkTokenHandled(ctx context.Context, tenantID, tokenID uuid.UUID) error {
	return nil
}
func (f *fakeWaitTokenRepo) FindExpiredTokens(ctx context.Context, limit int) ([]domain.StepWaitToken, error) {
	return nil, nil
}
func (f *fakeWaitTokenRepo) RecordOrphanEvent(ctx context.Context, e *domain.OrphanEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orphans = append(f.orphans, *e)
	return nil
}
func (f *fakeWaitTokenRepo) GetWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	return f.secret, nil
}
func (f *fakeWaitTokenRepo) SetWebhookSecret(ctx context.Context, tenantID uuid.UUID, secret string) error {
	f.secret = secret
	return nil
}

func newEventUsecase(t *testing.T, tokens *fakeWaitTokenRepo) (execution.ExecutionUseCase, *fakeRunRepo, *fakeStepRepo, *fakeEnqueuer, *recordingTxRunner) {
	t.Helper()
	fr := &fakeRunRepo{run: &domain.WorkflowRun{ID: uuid.New(), TenantID: uuid.New(), Status: domain.RunStatusWaiting}}
	fs := newFakeStepRepo([]string{"wait"})
	enq := &fakeEnqueuer{}
	tx := &recordingTxRunner{}
	uc := execution.NewExecutionUseCase(
		fakeWorkflowReader{wf: &domain.Workflow{ID: uuid.New(), TenantID: uuid.New(), CurrentVersionID: ptr(uuid.New())}},
		fr, fs, nil, fakeGraph{}, enq, &fakeAuditRepo{}, tx, nil, tokens,
		execution.ExecutionConfig{},
	)
	return uc, fr, fs, enq, tx
}

// Phase 7: HandleEvent resolves the matching token, succeeds the parked step,
// and re-enqueues the run.
func TestHandleEvent_ResolvesToken(t *testing.T) {
	tokens := newFakeWaitTokenRepo()
	runID := uuid.New()
	stepID := uuid.New()
	tenantID := uuid.New()
	now := time.Now().Add(time.Hour)
	tokens.byKey["ORD-1"] = domain.StepWaitToken{
		ID: uuid.New(), TenantID: tenantID, WorkflowRunID: runID, StepRunID: stepID,
		CorrelationKey: "ORD-1", ExpiresAt: now,
	}

	uc, _, _, enq, tx := newEventUsecase(t, tokens)

	resolved, err := uc.HandleEvent(context.Background(), tenantID, "ORD-1", []byte(`{"paid":true}`))
	require.NoError(t, err)
	assert.True(t, resolved)
	assert.True(t, tx.called, "token resolution must be transactional")
	assert.Equal(t, 1, enq.calls, "the run must be re-enqueued after its token resolves")
}

// Phase 7: a redelivered event for an already-consumed token is a safe no-op.
func TestHandleEvent_DuplicateRedeliveryIsNoOp(t *testing.T) {
	tokens := newFakeWaitTokenRepo()
	tenantID := uuid.New()
	consumed := time.Now()
	tokens.byKey["K"] = domain.StepWaitToken{
		ID: uuid.New(), TenantID: tenantID, WorkflowRunID: uuid.New(), StepRunID: uuid.New(),
		CorrelationKey: "K", ExpiresAt: time.Now().Add(time.Hour), ConsumedAt: &consumed,
	}

	uc, _, _, enq, _ := newEventUsecase(t, tokens)

	resolved, err := uc.HandleEvent(context.Background(), tenantID, "K", []byte(`{}`))
	require.NoError(t, err)
	assert.True(t, resolved, "a matching token is handled even if already consumed")
	assert.Zero(t, enq.calls, "no re-enqueue for a duplicate")
}

// Phase 7: an event with no matching token reports resolved=false so the
// adapter dead-letters it.
func TestHandleEvent_NoTokenIsUnresolved(t *testing.T) {
	tokens := newFakeWaitTokenRepo()
	uc, _, _, enq, _ := newEventUsecase(t, tokens)

	resolved, err := uc.HandleEvent(context.Background(), uuid.New(), "NO-SUCH-KEY", []byte(`{}`))
	require.NoError(t, err)
	assert.False(t, resolved)
	assert.Zero(t, enq.calls)
}

func TestRecordOrphanEvent_DeadLetters(t *testing.T) {
	tokens := newFakeWaitTokenRepo()
	uc, _, _, _, _ := newEventUsecase(t, tokens)
	tenantID := uuid.New()

	err := uc.RecordOrphanEvent(context.Background(), tenantID, "K", []byte(`{"a":1}`), "no matching token")
	require.NoError(t, err)
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	require.Len(t, tokens.orphans, 1)
	assert.Equal(t, "K", tokens.orphans[0].CorrelationKey)
	assert.Equal(t, "no matching token", tokens.orphans[0].Reason)
}

// Phase 8 / Log Writer: All 7 transitions produce durable execution logs with
// accurate level, message, and step_run_id attribution.
func TestCoordinator_ExecutionLogs_RecordedPerTransition(t *testing.T) {
	t.Run("step_waiting_and_run_parked", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"waitNode", domain.NodeTypeEventWait, map[string]any{"correlationKey": "KEY-123", "timeout": "1h"}},
		}, nil)
		logs := &fakeLogs{}
		c, fr, fs := coordWithLogs(t, graph, []string{"waitNode"}, nil, defaultRetry(), logs)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		entries := logs.getEntries()
		require.NotEmpty(t, entries)

		var waitLog *domain.ExecutionLog
		for _, e := range entries {
			if e.Message == "run parked on wait token" {
				waitLog = e
				break
			}
		}
		require.NotNil(t, waitLog, "must record 'run parked on wait token'")
		assert.Equal(t, domain.LogLevelInfo, waitLog.Level)
		require.NotNil(t, waitLog.StepRunID)
		assert.Equal(t, fs.steps["waitNode"].ID, *waitLog.StepRunID)
		assert.Equal(t, fr.run.ID, waitLog.WorkflowRunID)
		assert.Equal(t, fr.run.TenantID, waitLog.TenantID)
	})

	t.Run("step_succeeded_and_run_succeeded", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		}, nil)
		logs := &fakeLogs{}
		c, fr, fs := coordWithLogs(t, graph, []string{"a"}, nil, defaultRetry(), logs)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		entries := logs.getEntries()
		require.Len(t, entries, 2)

		// 1. step succeeded
		assert.Equal(t, domain.LogLevelInfo, entries[0].Level)
		assert.Equal(t, "step succeeded", entries[0].Message)
		require.NotNil(t, entries[0].StepRunID)
		assert.Equal(t, fs.steps["a"].ID, *entries[0].StepRunID)

		// 2. run succeeded
		assert.Equal(t, domain.LogLevelInfo, entries[1].Level)
		assert.Equal(t, "run succeeded", entries[1].Message)
		assert.Nil(t, entries[1].StepRunID)
		assert.Equal(t, fr.run.ID, entries[1].WorkflowRunID)
	})

	t.Run("step_retrying", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"retryNode", domain.NodeTypeDelay, map[string]any{"seconds": -1}},
		}, nil)
		logs := &fakeLogs{}
		c, fr, fs := coordWithLogs(t, graph, []string{"retryNode"}, nil, engine.RetryPolicy{
			MaxAttempts: 2,
			BaseDelay:   time.Millisecond,
			MaxDelay:    5 * time.Millisecond,
		}, logs)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		entries := logs.getEntries()
		var retryLog *domain.ExecutionLog
		for _, e := range entries {
			if e.Message == "step scheduled for retry" {
				retryLog = e
				break
			}
		}
		require.NotNil(t, retryLog, "must record 'step scheduled for retry'")
		assert.Equal(t, domain.LogLevelWarn, retryLog.Level)
		require.NotNil(t, retryLog.StepRunID)
		assert.Equal(t, fs.steps["retryNode"].ID, *retryLog.StepRunID)
	})

	t.Run("step_failed_and_run_failed", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"failNode", domain.NodeTypeDelay, map[string]any{"seconds": -1}},
		}, nil)
		logs := &fakeLogs{}
		c, fr, fs := coordWithLogs(t, graph, []string{"failNode"}, nil, engine.RetryPolicy{
			MaxAttempts: 1,
			BaseDelay:   time.Millisecond,
			MaxDelay:    time.Millisecond,
		}, logs)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		entries := logs.getEntries()
		require.Len(t, entries, 2)

		// 1. step failed
		assert.Equal(t, domain.LogLevelError, entries[0].Level)
		assert.Equal(t, "step failed", entries[0].Message)
		require.NotNil(t, entries[0].StepRunID)
		assert.Equal(t, fs.steps["failNode"].ID, *entries[0].StepRunID)
		assert.Contains(t, string(entries[0].Context), "message")

		// 2. run failed
		assert.Equal(t, domain.LogLevelError, entries[1].Level)
		assert.Equal(t, "run failed", entries[1].Message)
		assert.Nil(t, entries[1].StepRunID)
	})

	t.Run("run_stuck_liveness_check", func(t *testing.T) {
		// Graph only defines node 'a', but step repo has 'a' and 'b'.
		// 'a' succeeds, 'b' remains pending and unreachable, causing run stuck.
		graph := buildGraph([]nodeDef{
			{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		}, nil)
		logs := &fakeLogs{}
		c, fr, _ := coordWithLogs(t, graph, []string{"a", "b"}, nil, defaultRetry(), logs)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		entries := logs.getEntries()
		var stuckLog *domain.ExecutionLog
		for _, e := range entries {
			if e.Message == "run stuck: pending steps with no active path, marked failed" {
				stuckLog = e
				break
			}
		}
		require.NotNil(t, stuckLog, "must record stuck run liveness failure")
		assert.Equal(t, domain.LogLevelWarn, stuckLog.Level)
		assert.Nil(t, stuckLog.StepRunID)
		assert.Equal(t, fr.run.ID, stuckLog.WorkflowRunID)
	})
}

// Phase 8 / Log Writer: Append error is swallowed and never fails execution.
func TestCoordinator_ExecutionLogs_AppendErrorDoesNotFailExecution(t *testing.T) {
	graph := buildGraph([]nodeDef{
		{"a", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
	}, nil)
	logs := &fakeLogs{err: errors.New("database connection refused")}
	c, fr, fs := coordWithLogs(t, graph, []string{"a"}, nil, defaultRetry(), logs)

	err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
	require.NoError(t, err, "Append error must not propagate or fail HandleRun")

	assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
	assert.Equal(t, engine.StepStatusSucceeded, fs.steps["a"].Status)
	assert.GreaterOrEqual(t, logs.calls, 2, "Append must have been called despite errors")
}

// Phase 8: Real-Time Monitoring Events are emitted for coordinator transitions.
func TestCoordinator_RealTimeEvents_PublishedPerTransition(t *testing.T) {
	t.Run("step_started_completed_and_run_completed", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"node1", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		}, nil)
		events := &fakeEvents{}
		c, fr, fs := coordWithEvents(t, graph, []string{"node1"}, nil, defaultRetry(), events)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
		assert.Equal(t, engine.StepStatusSucceeded, fs.steps["node1"].Status)

		evList := events.getEvents()
		// Expected sequence:
		// 1. workflow.run.started
		// 2. step.started
		// 3. step.completed
		// 4. workflow.run.completed
		require.Len(t, evList, 4)

		assert.Equal(t, domain.EventRunStarted, evList[0].Type)
		assert.Equal(t, fr.run.ID, *evList[0].RunID)
		assert.Nil(t, evList[0].StepID)

		assert.Equal(t, domain.EventStepStarted, evList[1].Type)
		assert.Equal(t, fr.run.ID, *evList[1].RunID)
		assert.Equal(t, fs.steps["node1"].ID, *evList[1].StepID)

		assert.Equal(t, domain.EventStepCompleted, evList[2].Type)
		assert.Equal(t, fr.run.ID, *evList[2].RunID)
		assert.Equal(t, fs.steps["node1"].ID, *evList[2].StepID)

		assert.Equal(t, domain.EventRunCompleted, evList[3].Type)
		assert.Equal(t, fr.run.ID, *evList[3].RunID)
		assert.Nil(t, evList[3].StepID)
	})

	t.Run("step_retrying_failed_and_run_failed", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"failNode", domain.NodeTypeTransform, map[string]any{"expression": "bad-syntax"}},
		}, nil)
		events := &fakeEvents{}
		// Retry with maxAttempts: 2
		c, fr, fs := coordWithEvents(t, graph, []string{"failNode"}, nil, engine.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}, events)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err)

		evList := events.getEvents()
		// Sequence:
		// 1. workflow.run.started
		// 2. step.started
		// 3. step.retrying
		// 4. step.failed
		// 5. workflow.run.failed
		require.Len(t, evList, 5)

		assert.Equal(t, domain.EventRunStarted, evList[0].Type)
		assert.Equal(t, domain.EventStepStarted, evList[1].Type)
		assert.Equal(t, domain.EventStepRetrying, evList[2].Type)
		assert.Equal(t, fs.steps["failNode"].ID, *evList[2].StepID)

		assert.Equal(t, domain.EventStepFailed, evList[3].Type)
		assert.Equal(t, fs.steps["failNode"].ID, *evList[3].StepID)

		assert.Equal(t, domain.EventRunFailed, evList[4].Type)
		assert.Nil(t, evList[4].StepID)
	})

	t.Run("publish_error_does_not_fail_execution", func(t *testing.T) {
		graph := buildGraph([]nodeDef{
			{"node1", domain.NodeTypeDelay, map[string]any{"seconds": 0}},
		}, nil)
		events := &fakeEvents{err: errors.New("redis pubsub connection error")}
		c, fr, fs := coordWithEvents(t, graph, []string{"node1"}, nil, defaultRetry(), events)

		err := c.HandleRun(context.Background(), fr.run.TenantID, fr.run.ID)
		require.NoError(t, err, "Event publishing error must be swallowed and never fail execution (D-4)")

		assert.Equal(t, []string{domain.RunStatusSucceeded}, fr.statuses)
		assert.Equal(t, engine.StepStatusSucceeded, fs.steps["node1"].Status)
	})
}

// Package execution owns workflow run and step persistence, the coordinator
// that drives internal/engine over one run, and the use cases Phase 6's HTTP
// handlers call.
//
// The coordinator is pure orchestration: every decision (what is ready, what is
// skipped, whether a transition is legal) delegates to internal/engine. It does
// the I/O — load persisted state, claim steps, run executors, persist results.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/metrics"
)

// GraphLoader loads a version's persisted graph for execution.
type GraphLoader interface {
	LoadGraph(ctx context.Context, tenantID, versionID uuid.UUID) ([]domain.WorkflowNode, []domain.WorkflowEdge, error)
}

// CoordinatorConfig wires the coordinator's runtime knobs.
type CoordinatorConfig struct {
	Concurrency int
	Lease       time.Duration
	// TickIdleDelay is how long HandleRun pauses between ticks when it made no
	// progress but the run is not finished (e.g. a step stuck running that the
	// reaper has not yet reset). Prevents a hot spin against the database. Zero
	// defaults to 1s.
	TickIdleDelay time.Duration
	Retry         engine.RetryPolicy
	Timeout       engine.TimeoutPolicy
	MaxBodyBytes  int64
	WorkerID      string
	Logger        *slog.Logger
	// HTTPClient must be the SSRF-pinned client from internal/platform/safehttp;
	// nil makes every HTTP step fail cleanly.
	HTTPClient *http.Client
	// Metrics may be nil; when set, the coordinator emits step and run telemetry.
	Metrics *metrics.Metrics
	// Publisher may be nil; EVENT_PUBLISH steps fail cleanly without one.
	Publisher executor.EventPublisher
	// WaitTokens is the store EVENT_WAIT steps use to park runs; nil makes them
	// fail cleanly. The wait bounds bound a tenant's parked runs.
	WaitTokens          executor.WaitTokenStore
	MaxWaitPerTenant    int
	DefaultWaitDuration time.Duration
	MaxWaitDuration     time.Duration
}

// Coordinator drives one run to a terminal state by repeatedly asking the
// engine what to do and persisting the outcome. It owns no in-memory run state
// across calls: a worker that crashes mid-run is resumable by a different
// worker with no shared state.
type Coordinator struct {
	runs  RunRepository
	steps StepRunRepository
	logs  LogRepository
	graph GraphLoader
	execs *executor.Registry
	cfg   CoordinatorConfig
	rnd   *rand.Rand
}

// NewCoordinator builds a coordinator. Concurrency is bounded by cfg.Concurrency;
// if zero it defaults to 10.
func NewCoordinator(runs RunRepository, steps StepRunRepository, logs LogRepository,
	graph GraphLoader, execs *executor.Registry, cfg CoordinatorConfig) *Coordinator {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 10
	}
	if cfg.TickIdleDelay <= 0 {
		cfg.TickIdleDelay = time.Second
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = "worker"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Coordinator{
		runs:  runs,
		steps: steps,
		logs:  logs,
		graph: graph,
		execs: execs,
		cfg:   cfg,
		rnd:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// HandleRun is the queue handler: claim the run atomically, then drive it to a
// terminal state. A nil claim (duplicate delivery) is a silent no-op — the
// normal outcome, not an error.
func (c *Coordinator) HandleRun(ctx context.Context, tenantID, runID uuid.UUID) error {
	run, err := c.runs.ClaimRun(ctx, tenantID, runID, c.cfg.WorkerID, c.cfg.Lease)
	if err != nil {
		return fmt.Errorf("claim run: %w", err)
	}
	if run == nil {
		c.cfg.Logger.Debug("duplicate run delivery, claim yielded no row", slog.String("runID", runID.String()))
		return nil
	}

	// One cancellable context shared by the tick loop and the heartbeat: when the
	// heartbeat learns the lease was lost, it cancels workCtx and the loop stops.
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()

	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go c.heartbeat(workCtx, &hbWG, tenantID, runID, cancelWork)
	defer hbWG.Wait() // LIFO: cancelWork below runs first, so the goroutine exits
	defer cancelWork()

	for {
		if err := workCtx.Err(); err != nil {
			return err
		}
		more, err := c.Tick(workCtx, tenantID, runID)
		if err != nil {
			if errors.Is(err, ErrLeaseLost) {
				c.cfg.Logger.Warn("lease lost while ticking, abandoning run", slog.String("runID", runID.String()))
				return nil
			}
			// Transient failure: leave the run 'running' and let the lease expire;
			// the reaper reclaims and re-enqueues it. Do not fail the run on a DB
			// blip, and do not let asynq retry (the claim would no-op anyway).
			c.cfg.Logger.Error("tick failed", slog.String("runID", runID.String()), slog.Any("error", err))
			return nil
		}
		if more {
			continue
		}

		// No progress this round. If the run reached a terminal state, we are
		// done. Otherwise it is parked (e.g. a step stuck running that the reaper
		// has not yet reset, or a Phase 7 waiting step): pause rather than spin.
		run, err := c.runs.GetRun(workCtx, tenantID, runID)
		if err != nil {
			return nil
		}
		if run.Status != domain.RunStatusRunning {
			return nil
		}
		select {
		case <-time.After(c.cfg.TickIdleDelay):
		case <-workCtx.Done():
			return workCtx.Err()
		}
	}
}

// heartbeat extends the run's lease while work is in flight. On ErrLeaseLost —
// the run finished or another worker owns it — it cancels the shared work
// context so HandleRun stops; the worker must not touch a run it no longer owns.
func (c *Coordinator) heartbeat(ctx context.Context, wg *sync.WaitGroup, tenantID, runID uuid.UUID, cancelWork func()) {
	defer wg.Done()
	interval := c.cfg.Lease / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.runs.ExtendLease(ctx, tenantID, runID, c.cfg.WorkerID, c.cfg.Lease); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					c.cfg.Logger.Warn("lease lost, abandoning run", slog.String("runID", runID.String()))
					cancelWork()
					return
				}
				c.cfg.Logger.Warn("lease heartbeat failed", slog.String("runID", runID.String()), slog.Any("error", err))
			}
		}
	}
}

// Tick advances one run by one scheduling round and reports whether more work
// remains. It rebuilds the scope from persisted step outputs every call.
func (c *Coordinator) Tick(ctx context.Context, tenantID, runID uuid.UUID) (bool, error) {
	run, err := c.runs.GetRun(ctx, tenantID, runID)
	if err != nil {
		return false, fmt.Errorf("load run: %w", err)
	}
	if run.Status != domain.RunStatusRunning {
		return false, nil // cancelled or already finished — nothing to do
	}
	// The fencing token: if we no longer own the run, we must stop before
	// touching anything. A lease that is never validated on use is not a lease.
	if run.ClaimedBy == nil || *run.ClaimedBy != c.cfg.WorkerID {
		return false, ErrLeaseLost
	}

	graph, err := c.loadGraph(ctx, tenantID, run.WorkflowVersionID)
	if err != nil {
		return false, err
	}

	stepRuns, err := c.steps.ListStepRuns(ctx, tenantID, runID)
	if err != nil {
		return false, fmt.Errorf("load step runs: %w", err)
	}
	states, scope, err := buildScope(run, stepRuns)
	if err != nil {
		return false, err
	}

	ready, skipped, err := engine.CalculateReadyNodes(graph, states, scope)
	if err != nil {
		return false, err
	}

	if len(skipped) > 0 {
		if err := c.steps.MarkStepsSkipped(ctx, tenantID, runID, skipped); err != nil {
			return false, fmt.Errorf("mark skipped: %w", err)
		}
		for _, k := range skipped {
			states[k] = engine.StepStatusSkipped
		}
	}

	// Make the engine's ready set claimable, then execute the wave.
	if len(ready) > 0 {
		if err := c.steps.MarkStepsReady(ctx, tenantID, runID, ready); err != nil {
			return false, fmt.Errorf("mark ready: %w", err)
		}
	}

	didWork, err := c.executeWave(ctx, tenantID, runID, graph, scope)
	if err != nil {
		return false, err
	}
	if didWork {
		return true, nil
	}

	// Nothing was dispatchable. Decide whether the run is done or stuck; either
	// way this worker made no progress, so report false and let HandleRun idle
	// rather than spin.
	if err := c.finishRun(ctx, tenantID, runID); err != nil {
		return false, err
	}
	return false, nil
}

// executeWave claims ready steps and runs them under bounded concurrency,
// re-dispatching failed steps while retries remain. Returns true if any step
// executed.
func (c *Coordinator) executeWave(ctx context.Context, tenantID, runID uuid.UUID,
	graph domain.Graph, scope engine.Scope) (bool, error) {

	byKey := make(map[string]domain.NodeInput, len(graph.Nodes))
	for _, n := range graph.Nodes {
		byKey[n.NodeKey] = n
	}

	didWork := false
	for {
		claimed, err := c.steps.ClaimReadySteps(ctx, tenantID, runID, c.cfg.Concurrency)
		if err != nil {
			return didWork, fmt.Errorf("claim ready steps: %w", err)
		}
		if len(claimed) == 0 {
			return didWork, nil
		}
		didWork = true

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(c.cfg.Concurrency)
		for _, step := range claimed {
			step := step
			g.Go(func() error {
				return c.runStepWithRetry(gctx, tenantID, runID, step, byKey, scope)
			})
		}
		if err := g.Wait(); err != nil {
			// Real infrastructure error (a step failure is persisted inside
			// runStepWithRetry and returns nil). Bubble up for lease recovery.
			return didWork, err
		}
	}
}

// runStepWithRetry executes one step, re-dispatching on failure until attempts
// are exhausted. Every status write is gated by engine.CanTransition.
func (c *Coordinator) runStepWithRetry(ctx context.Context, tenantID, runID uuid.UUID,
	step domain.StepRun, nodes map[string]domain.NodeInput, scope engine.Scope) error {

	node, ok := nodes[step.NodeKey]
	if !ok {
		return fmt.Errorf("step %q has no graph node", step.NodeKey)
	}
	ex, ok := c.execs.Get(node.NodeType)
	if !ok {
		return fmt.Errorf("no executor for node type %q", node.NodeType)
	}

	attempt := step.AttemptCount + 1
	maxAttempts := c.cfg.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		timeout, err := c.cfg.Timeout.EffectiveTimeout(node)
		if err != nil {
			return err
		}
		start := time.Now()
		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		out, execErr := ex.Execute(stepCtx, executor.Input{
			Node:         node,
			Scope:        scope,
			HTTP:         c.cfg.HTTPClient,
			MaxBodyBytes: c.cfg.MaxBodyBytes,
			Publisher:    c.cfg.Publisher,
			TenantID:     tenantID,
			RunID:        runID,
			StepRunID:    step.ID,
			WaitTokens:   c.cfg.WaitTokens,
			WaitConfig: executor.WaitConfig{
				MaxPerTenant:    c.cfg.MaxWaitPerTenant,
				DefaultDuration: c.cfg.DefaultWaitDuration,
				MaxDuration:     c.cfg.MaxWaitDuration,
			},
		})
		cancel()
		duration := time.Since(start)

		if execErr == nil && out.Waiting {
			// EVENT_WAIT parked the run: move both the step and the run to
			// 'waiting' and release the run — no goroutine may be held while a
			// workflow sleeps, whatever the wait duration. The run is woken when
			// the inbound event resolves the token.
			outputJSON, err := json.Marshal(out.Data)
			if err != nil {
				return fmt.Errorf("marshal step output: %w", err)
			}
			if !engine.CanTransition(engine.StepStatusRunning, engine.StepStatusWaiting) {
				return errors.New("illegal transition running -> waiting")
			}
			if err := c.steps.UpdateStepResult(ctx, tenantID, runID, step.NodeKey, StepUpdate{
				Status:       engine.StepStatusWaiting,
				AttemptCount: attempt,
				Output:       outputJSON,
			}); err != nil {
				return fmt.Errorf("persist step waiting: %w", err)
			}
			if !canTransitionRun(domain.RunStatusRunning, domain.RunStatusWaiting) {
				return errors.New("illegal run transition running -> waiting")
			}
			if err := c.runs.UpdateRunStatus(ctx, tenantID, runID, domain.RunStatusWaiting, false); err != nil {
				return fmt.Errorf("persist run waiting: %w", err)
			}
			c.cfg.Logger.Info("run parked on wait token", slog.String("nodeKey", step.NodeKey), slog.String("runID", runID.String()))
			return nil
		}

		if execErr == nil {
			outputJSON, err := json.Marshal(out.Data)
			if err != nil {
				return fmt.Errorf("marshal step output: %w", err)
			}
			if !engine.CanTransition(engine.StepStatusRunning, engine.StepStatusSucceeded) {
				return errors.New("illegal transition running -> succeeded")
			}
			now := time.Now()
			if err := c.steps.UpdateStepResult(ctx, tenantID, runID, step.NodeKey, StepUpdate{
				Status:       engine.StepStatusSucceeded,
				AttemptCount: attempt,
				Output:       outputJSON,
				FinishedAt:   &now,
			}); err != nil {
				return fmt.Errorf("persist step success: %w", err)
			}
			if c.cfg.Metrics != nil {
				c.cfg.Metrics.Steps.WithLabelValues(tenantID.String(), node.NodeType, engine.StepStatusSucceeded).Inc()
				c.cfg.Metrics.StepDuration.WithLabelValues(node.NodeType, engine.StepStatusSucceeded).Observe(duration.Seconds())
			}
			c.cfg.Logger.Info("step succeeded", slog.String("nodeKey", step.NodeKey), slog.Int("attempt", attempt))
			return nil
		}

		// The run's context was cancelled (lease lost or shutdown). Leave the
		// step 'running' for the reaper to reset — writing a status here would
		// mutate a run we no longer own. A step timeout is a real failure and is
		// handled below, not here.
		if errors.Is(execErr, context.Canceled) {
			return execErr
		}

		errPayload := mustJSON(map[string]any{"message": execErr.Error()})
		now := time.Now()

		if attempt >= maxAttempts {
			if !engine.CanTransition(engine.StepStatusRunning, engine.StepStatusFailed) {
				return errors.New("illegal transition running -> failed")
			}
			if err := c.steps.UpdateStepResult(ctx, tenantID, runID, step.NodeKey, StepUpdate{
				Status:       engine.StepStatusFailed,
				AttemptCount: attempt,
				ErrorPayload: errPayload,
				FinishedAt:   &now,
			}); err != nil {
				return fmt.Errorf("persist step failure: %w", err)
			}
			if c.cfg.Metrics != nil {
				c.cfg.Metrics.Steps.WithLabelValues(tenantID.String(), node.NodeType, engine.StepStatusFailed).Inc()
				c.cfg.Metrics.StepDuration.WithLabelValues(node.NodeType, engine.StepStatusFailed).Observe(duration.Seconds())
			}
			c.cfg.Logger.Warn("step failed", slog.String("nodeKey", step.NodeKey), slog.Int("attempt", attempt), slog.Any("error", execErr))
			return nil
		}

		if !engine.CanTransition(engine.StepStatusRunning, engine.StepStatusRetrying) {
			return errors.New("illegal transition running -> retrying")
		}
		if err := c.steps.UpdateStepResult(ctx, tenantID, runID, step.NodeKey, StepUpdate{
			Status:       engine.StepStatusRetrying,
			AttemptCount: attempt,
			ErrorPayload: errPayload,
		}); err != nil {
			return fmt.Errorf("persist retry: %w", err)
		}
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.Retries.WithLabelValues(tenantID.String(), node.NodeType).Inc()
		}

		backoff := c.cfg.Retry.NextBackoff(attempt, c.rnd)
		c.cfg.Logger.Warn("step scheduled for retry", slog.String("nodeKey", step.NodeKey),
			slog.Int("attempt", attempt), slog.Duration("backoff", backoff))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		attempt++

		if !engine.CanTransition(engine.StepStatusRetrying, engine.StepStatusRunning) {
			return errors.New("illegal transition retrying -> running")
		}
		if err := c.steps.UpdateStepStatus(ctx, tenantID, runID, step.NodeKey, engine.StepStatusRunning); err != nil {
			return fmt.Errorf("re-dispatch retry: %w", err)
		}
	}
}

// finishRun decides the run's terminal state once no step is dispatchable. It
// writes the terminal status when the run is done; a run with an active step it
// cannot claim (waiting for the reaper, or a Phase 7 wait) is left as-is.
func (c *Coordinator) finishRun(ctx context.Context, tenantID, runID uuid.UUID) error {
	stepRuns, err := c.steps.ListStepRuns(ctx, tenantID, runID)
	if err != nil {
		return fmt.Errorf("load step runs for finish: %w", err)
	}

	anyActive := false
	anyFailed := false
	anyPending := false
	for _, s := range stepRuns {
		switch s.Status {
		case engine.StepStatusRunning, engine.StepStatusReady, engine.StepStatusWaiting, engine.StepStatusRetrying:
			anyActive = true
		case engine.StepStatusFailed:
			anyFailed = true
		case engine.StepStatusPending:
			anyPending = true
		}
	}

	if anyActive {
		return nil // the reaper (or a Phase 7 listener) will resolve it; do not spin
	}

	if anyPending && !anyFailed {
		// Liveness check: pending steps with nothing running/ready means the run
		// is stuck (no path will ever arrive). Mark it failed rather than hang.
		if err := c.runs.UpdateRunStatus(ctx, tenantID, runID, domain.RunStatusFailed, true); err != nil {
			return fmt.Errorf("mark stuck run failed: %w", err)
		}
		c.cfg.Logger.Warn("run stuck: pending steps with no active path, marked failed",
			slog.String("runID", runID.String()))
		return nil
	}

	status := domain.RunStatusSucceeded
	if anyFailed {
		status = domain.RunStatusFailed
	}
	if !canTransitionRun(domain.RunStatusRunning, status) {
		return errors.New("illegal run terminal transition")
	}
	if err := c.runs.UpdateRunStatus(ctx, tenantID, runID, status, true); err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.RunsFinished.WithLabelValues(tenantID.String(), status).Inc()
	}
	return nil
}

func (c *Coordinator) loadGraph(ctx context.Context, tenantID, versionID uuid.UUID) (domain.Graph, error) {
	nodes, edges, err := c.graph.LoadGraph(ctx, tenantID, versionID)
	if err != nil {
		return domain.Graph{}, fmt.Errorf("load graph: %w", err)
	}
	g := domain.FromPersisted(nodes, edges)
	g.Normalize()
	if err := engine.ValidateDAG(g); err != nil {
		return domain.Graph{}, fmt.Errorf("validate graph: %w", err)
	}
	return g, nil
}

// buildScope reconstructs the engine scope from the run's input context and the
// completed step outputs — never carried in memory across ticks.
func buildScope(run *domain.WorkflowRun, stepRuns []domain.StepRun) (map[string]string, engine.Scope, error) {
	states := make(map[string]string, len(stepRuns))
	steps := make(map[string]engine.StepOutput, len(stepRuns))

	for _, s := range stepRuns {
		states[s.NodeKey] = s.Status
		var output map[string]any
		if len(s.OutputPayload) > 0 {
			if err := json.Unmarshal(s.OutputPayload, &output); err != nil {
				return nil, engine.Scope{}, fmt.Errorf("unmarshal output for step %q: %w", s.NodeKey, err)
			}
		}
		steps[s.NodeKey] = engine.StepOutput{Status: s.Status, Output: output}
	}

	var trigger map[string]any
	if len(run.InputContext) > 0 {
		if err := json.Unmarshal(run.InputContext, &trigger); err != nil {
			return nil, engine.Scope{}, fmt.Errorf("unmarshal input context: %w", err)
		}
	}

	return states, engine.Scope{
		Trigger: trigger,
		Steps:   steps,
		Env:     map[string]string{},
	}, nil
}

// canTransitionRun gates the run-level state machine: pending→running (the
// claim), running→{succeeded, failed, canceled, timed_out, waiting}, and
// waiting→pending (woken by event or expired token).
func canTransitionRun(from, to string) bool {
	switch from {
	case domain.RunStatusPending:
		return to == domain.RunStatusRunning
	case domain.RunStatusRunning:
		switch to {
		case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCanceled,
			domain.RunStatusTimedOut, domain.RunStatusWaiting:
			return true
		}
	case domain.RunStatusWaiting:
		// An EVENT_WAIT run can be woken by an inbound event (→running via the
		// claim) or its token can expire (→pending so a worker re-claims and
		// walks the step's error path). It is never woken in place.
		return to == domain.RunStatusPending
	}
	return false
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

package execution_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/safehttp"
	"flowforge/internal/workflow"
)

// End-to-end: a run is driven to a terminal status against the real repositories.
func TestCoordinator_E2EExecution(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 2)
	// a -> b linear chain.
	_, err := pool.Exec(ctx, `INSERT INTO workflow_edges (id, tenant_id, workflow_version_id, from_node_id, to_node_id, branch) VALUES ($1,$2,$3,$4,$5,'default')`,
		uuid.New(), tenantID, verID, nodes[0].ID, nodes[1].ID)
	require.NoError(t, err)

	repo := execution.NewExecutionRepository(pool)
	run := &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err = repo.CreateRun(ctx, run)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))

	// Mark both nodes DELAY(0) so execution is instant and I/O-free.
	for i, n := range nodes {
		cfg, _ := json.Marshal(map[string]any{"seconds": 0})
		_, err := pool.Exec(ctx, `UPDATE workflow_nodes SET node_type = 'DELAY', config = $1 WHERE id = $2`, string(cfg), n.ID)
		require.NoError(t, err)
		_ = i
	}

	coord := execution.NewCoordinator(repo, repo, repo, workflow.NewVersionRepository(pool),
		executor.NewRegistry(nil, 1<<20, nil),
		execution.CoordinatorConfig{
			Concurrency: 4,
			Lease:       60 * time.Second,
			Retry:       engine.DefaultRetryPolicy(),
			Timeout:     engine.DefaultTimeoutPolicy(),
			WorkerID:    "it-worker",
			Logger:      slog.New(slog.DiscardHandler),
			HTTPClient:  safehttp.NewSSRFValidator(nil, true).Client(5 * time.Second),
		})

	require.NoError(t, coord.HandleRun(ctx, tenantID, run.ID))

	got, err := repo.GetRun(ctx, tenantID, run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunStatusSucceeded, got.Status, "run must reach a terminal status")
	steps, err := repo.ListStepRuns(ctx, tenantID, run.ID)
	require.NoError(t, err)
	assert.Len(t, steps, 2)
	for _, s := range steps {
		assert.Equal(t, engine.StepStatusSucceeded, s.Status)
	}
}

// Q-23: cancelling a run mid-flight stops dispatch of downstream steps and the
// run lands in canceled.
func TestCoordinator_CancelStopsDispatch(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 2)
	_, err := pool.Exec(ctx, `INSERT INTO workflow_edges (id, tenant_id, workflow_version_id, from_node_id, to_node_id, branch) VALUES ($1,$2,$3,$4,$5,'default')`,
		uuid.New(), tenantID, verID, nodes[0].ID, nodes[1].ID)
	require.NoError(t, err)

	// First node sleeps 2s (long enough to cancel mid-flight); second is instant.
	delayCfg, _ := json.Marshal(map[string]any{"seconds": 2})
	instantCfg, _ := json.Marshal(map[string]any{"seconds": 0})
	_, err = pool.Exec(ctx, `UPDATE workflow_nodes SET node_type='DELAY', config=$1 WHERE id=$2`, string(delayCfg), nodes[0].ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE workflow_nodes SET node_type='DELAY', config=$1 WHERE id=$2`, string(instantCfg), nodes[1].ID)
	require.NoError(t, err)

	repo := execution.NewExecutionRepository(pool)
	run := &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err = repo.CreateRun(ctx, run)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))

	uc := execution.NewExecutionUseCase(
		seedWorkflowReader{get: func() (*domain.Workflow, error) { return nil, nil }},
		repo, repo, repo, workflow.NewVersionRepository(pool), nil, nil,
		&recordingTxRunner{}, nil, repo, execution.ExecutionConfig{})

	coord := execution.NewCoordinator(repo, repo, repo, workflow.NewVersionRepository(pool),
		executor.NewRegistry(nil, 1<<20, nil),
		execution.CoordinatorConfig{
			Concurrency: 2,
			Lease:       60 * time.Second,
			Retry:       engine.DefaultRetryPolicy(),
			Timeout:     engine.DefaultTimeoutPolicy(),
			WorkerID:    "it-worker",
			Logger:      slog.New(slog.DiscardHandler),
		})

	done := make(chan error, 1)
	go func() { done <- coord.HandleRun(ctx, tenantID, run.ID) }()

	time.Sleep(300 * time.Millisecond) // let the first step claim and start sleeping
	require.NoError(t, uc.CancelRun(ctx, tenantID, run.ID))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}

	got, err := repo.GetRun(ctx, tenantID, run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunStatusCanceled, got.Status)
	steps, err := repo.ListStepRuns(ctx, tenantID, run.ID)
	require.NoError(t, err)
	for _, s := range steps {
		if s.NodeKey == nodes[0].NodeKey {
			continue // the in-flight step may finish before cancellation takes effect
		}
		assert.NotEqual(t, engine.StepStatusSucceeded, s.Status, "downstream step %s must not complete after cancel", s.NodeKey)
	}
}

type seedWorkflowReader struct {
	get func() (*domain.Workflow, error)
}

func (s seedWorkflowReader) GetWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.Workflow, error) {
	return s.get()
}

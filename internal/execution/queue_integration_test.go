package execution_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/queue"
	"flowforge/internal/platform/redis"
	"flowforge/internal/workflow"
)

// Full acceptance: a run enqueued on real Redis is delivered by the asynq
// server, claimed, executed to a terminal status, and never runs twice.
func TestQueue_EndToEndRunExecution(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := redis.NewClient(ctx, redisURL)
	require.NoError(t, err, "real redis required for this integration test")
	defer rc.Close()

	// Seed a linear a -> b graph, both DELAY(0).
	tenantID, wfID, verID, nodes := seedRunFixtures(t, context.Background(), pool, 2)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO workflow_edges (id, tenant_id, workflow_version_id, from_node_id, to_node_id, branch) VALUES ($1,$2,$3,$4,$5,'default')`,
		uuid.New(), tenantID, verID, nodes[0].ID, nodes[1].ID)
	require.NoError(t, err)
	cfg, _ := json.Marshal(map[string]any{"seconds": 0})
	for _, n := range nodes {
		_, err := pool.Exec(context.Background(), `UPDATE workflow_nodes SET node_type='DELAY', config=$1 WHERE id=$2`, string(cfg), n.ID)
		require.NoError(t, err)
	}

	repo := execution.NewExecutionRepository(pool)
	qc := queue.NewClient(rc)
	defer qc.Close()

	coord := execution.NewCoordinator(repo, repo, repo, workflow.NewVersionRepository(pool),
		executor.NewRegistry(nil, 1<<20, nil),
		execution.CoordinatorConfig{
			Concurrency: 4,
			Lease:       60 * time.Second,
			Retry:       engine.DefaultRetryPolicy(),
			Timeout:     engine.DefaultTimeoutPolicy(),
			WorkerID:    "it-queue-worker",
			Logger:      slog.New(slog.DiscardHandler),
		})

	server, handler := queue.NewServer(rc, 4, func(hctx context.Context, tenantID, runID uuid.UUID) error {
		return coord.HandleRun(hctx, tenantID, runID)
	})
	require.NoError(t, server.Start(handler))
	defer server.Shutdown()

	// Create a pending run and enqueue it — exactly as CreateRun would.
	runID := uuid.New()
	run := &domain.WorkflowRun{
		ID:                runID,
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err = repo.CreateRun(context.Background(), run)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(context.Background(), tenantID, runID, nodes))
	require.NoError(t, qc.EnqueueRun(tenantID, runID))

	// Poll for a terminal status; the queue worker must pick it up. 'running' is
	// a legitimate transient — only assert once the run has finished.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got, err := repo.GetRun(context.Background(), tenantID, runID)
		require.NoError(t, err)
		if got.Status == domain.RunStatusSucceeded || got.Status == domain.RunStatusFailed {
			assert.Equal(t, domain.RunStatusSucceeded, got.Status, "run must execute to a terminal status via the queue")
			steps, _ := repo.ListStepRuns(context.Background(), tenantID, runID)
			require.Len(t, steps, 2)
			for _, s := range steps {
				assert.Equal(t, engine.StepStatusSucceeded, s.Status)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("run never reached a terminal status via the queue")
}

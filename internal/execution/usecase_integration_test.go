package execution_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/execution"
	"flowforge/internal/platform/audit"
	"flowforge/internal/platform/postgres"
	"flowforge/internal/workflow"
)

// End-to-end smoke of the real usecase against real Postgres: an idempotent
// trigger creates one row, and a retry produces a lineage-linked run whose
// succeeded steps are copied wholesale.
func TestExecutionUseCase_E2EIdempotentTriggerAndRetry(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 2)
	_, err := pool.Exec(ctx, `INSERT INTO workflow_edges (id, tenant_id, workflow_version_id, from_node_id, to_node_id, branch) VALUES ($1,$2,$3,$4,$5,'default')`,
		uuid.New(), tenantID, verID, nodes[0].ID, nodes[1].ID)
	require.NoError(t, err)
	// Mark node a succeeded (to be reused on retry), node b failed.
	_, err = pool.Exec(ctx, `UPDATE workflow_nodes SET node_type='DELAY', config='{"seconds":0}'`)
	require.NoError(t, err)

	repo := execution.NewExecutionRepository(pool)
	verRepo := workflow.NewVersionRepository(pool)
	uow := postgres.NewUnitOfWork(pool)
	auditRepo := audit.NewAuditRepository(pool)
	enq := &fakeEnqueuer{}
	// WorkflowReader: the real workflow usecase reads current version from the DB.
	wfRepo := workflow.NewWorkflowRepository(pool)
	wfUC := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, uow)

	uc := execution.NewExecutionUseCase(wfUC, repo, repo, repo, verRepo, enq, auditRepo, uow, nil, repo,
		execution.ExecutionConfig{AIMaxRetries: 1, AIRequestTimeout: 0})

	key := "smoke-" + uuid.New().String()

	// Publish the workflow so there is a current version.
	_, err = pool.Exec(ctx, `UPDATE workflows SET status='published', current_version_id=$1, current_version_number=1 WHERE id=$2`, verID, wfID)
	require.NoError(t, err)

	// The audit FK requires a real actor user.
	actorID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO users (id, tenant_id, email, password_hash, role) VALUES ($1,$2,$3,'x','viewer')`,
		actorID, tenantID, "smoke-"+actorID.String()+"@flowforge.test")
	require.NoError(t, err)

	run1, err := uc.CreateRun(ctx, execution.CreateRunCommand{TenantID: tenantID, WorkflowID: wfID, ActorID: actorID, IdempotencyKey: &key})
	require.NoError(t, err)
	require.Equal(t, 1, enq.calls, "first trigger enqueues once")

	run2, err := uc.CreateRun(ctx, execution.CreateRunCommand{TenantID: tenantID, WorkflowID: wfID, ActorID: actorID, IdempotencyKey: &key})
	require.NoError(t, err)
	assert.Equal(t, run1.ID, run2.ID, "duplicate idempotency key returns the same run")
	assert.Equal(t, 1, enq.calls, "duplicate trigger must not enqueue again")

	// Fail one step so a retry has something to re-execute.
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='failed', attempt_count=1, finished_at=NOW() WHERE workflow_run_id=$1 AND node_key=$2`, run1.ID, nodes[1].NodeKey)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='succeeded', attempt_count=1, started_at=NOW(), finished_at=NOW() WHERE workflow_run_id=$1 AND node_key=$2`, run1.ID, nodes[0].NodeKey)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE workflow_runs SET status='failed', finished_at=NOW() WHERE id=$1`, run1.ID)
	require.NoError(t, err)

	retried, err := uc.RetryRun(ctx, execution.RetryRunCommand{TenantID: tenantID, RunID: run1.ID, ActorID: actorID, RetryFailedStepsOnly: true})
	require.NoError(t, err)
	require.NotNil(t, retried.RetriedFromRunID)
	assert.Equal(t, run1.ID, *retried.RetriedFromRunID, "the retry must trace back to the original run")

	steps, err := repo.ListStepRuns(ctx, tenantID, retried.ID)
	require.NoError(t, err)
	require.Len(t, steps, 2)
	for _, s := range steps {
		if s.NodeKey == nodes[0].NodeKey {
			assert.Equal(t, "succeeded", s.Status, "a reused step is copied as succeeded")
			assert.NotNil(t, s.StartedAt, "a reused step keeps its timing")
		} else {
			assert.Equal(t, "pending", s.Status, "a failed step is re-seeded as pending")
			assert.Nil(t, s.StartedAt, "a re-seeded step shows no timing for work it did not do")
		}
	}
}

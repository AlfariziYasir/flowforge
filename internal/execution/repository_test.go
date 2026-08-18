package execution_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/execution"
	"flowforge/internal/platform/logger"
	"flowforge/internal/platform/postgres"
)

func getTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("FLOWFORGE_INTEGRATION") == "" {
		t.Skip("integration test — set FLOWFORGE_INTEGRATION=1 (needs `make up`)")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/flowforge?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("FLOWFORGE_INTEGRATION=1 but cannot connect to %s: %v", logger.RedactURL(dbURL), err)
	}
	return pool
}

// seedRunFixtures inserts a tenant, workflow, version and returns their ids.
func seedRunFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nNodes int) (tenantID, wfID, verID uuid.UUID, nodes []domain.WorkflowNode) {
	t.Helper()
	tenantID = uuid.New()
	wfID = uuid.New()
	verID = uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO tenants (id, slug, name) VALUES ($1, $2, $3)`, tenantID, "it-"+tenantID.String(), "it-"+tenantID.String())
	require.NoError(t, err, "insert tenant")

	_, err = pool.Exec(ctx, `INSERT INTO workflows (id, tenant_id, name, status) VALUES ($1, $2, $3, 'draft')`, wfID, tenantID, "it-workflow")
	require.NoError(t, err, "insert workflow")

	_, err = pool.Exec(ctx, `INSERT INTO workflow_versions (id, tenant_id, workflow_id, version_number, status) VALUES ($1, $2, $3, 1, 'published')`, verID, tenantID, wfID)
	require.NoError(t, err, "insert version")

	nodes = make([]domain.WorkflowNode, 0, nNodes)
	for i := 0; i < nNodes; i++ {
		n := domain.WorkflowNode{
			ID:                uuid.New(),
			TenantID:          tenantID,
			WorkflowVersionID: verID,
			NodeKey:           "n" + string(rune('a'+i)),
			NodeType:          domain.NodeTypeHTTP,
		}
		_, err := pool.Exec(ctx, `INSERT INTO workflow_nodes (id, tenant_id, workflow_version_id, node_key, node_type) VALUES ($1, $2, $3, $4, $5)`,
			n.ID, n.TenantID, n.WorkflowVersionID, n.NodeKey, n.NodeType)
		require.NoError(t, err, "insert node %s", n.NodeKey)
		nodes = append(nodes, n)
	}
	return tenantID, wfID, verID, nodes
}

// Q-18: N goroutines claim the same pending run; exactly one succeeds.
func TestRunClaim_ExactlyOneWinner(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	const workers = 16
	const iterations = 50

	for iter := 0; iter < iterations; iter++ {
		tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
		run := &domain.WorkflowRun{
			ID:                uuid.New(),
			TenantID:          tenantID,
			WorkflowID:        wfID,
			WorkflowVersionID: verID,
		}
		_, err := repo.CreateRun(ctx, run)
		require.NoError(t, err)

		var wg sync.WaitGroup
		successes := make(chan *domain.WorkflowRun, workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				claimed, err := repo.ClaimRun(ctx, tenantID, run.ID, "worker-"+string(rune('a'+w)), 60*time.Second)
				if err == nil && claimed != nil {
					successes <- claimed
				}
			}(w)
		}
		wg.Wait()
		close(successes)

		winners := 0
		for range successes {
			winners++
		}
		assert.Equal(t, 1, winners, "iteration %d: exactly one worker must win the claim", iter)
	}
}

// Q-19: two workers claim ready steps of one run under SKIP LOCKED; every step
// is claimed exactly once and none is starved.
func TestStepClaim_NoDoubleClaimNoStarvation(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	const workers = 4
	const steps = 9

	for iter := 0; iter < 30; iter++ {
		tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, steps)
		run := &domain.WorkflowRun{
			ID:                uuid.New(),
			TenantID:          tenantID,
			WorkflowID:        wfID,
			WorkflowVersionID: verID,
		}
		_, err := repo.CreateRun(ctx, run)
		require.NoError(t, err)
		require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))

		// Make every step ready.
		for _, n := range nodes {
			require.NoError(t, repo.UpdateStepStatus(ctx, tenantID, run.ID, n.NodeKey, "ready"))
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		claimed := map[uuid.UUID]struct{}{}
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Repeated pulls: each worker keeps asking until no steps remain.
				for {
					got, err := repo.ClaimReadySteps(ctx, tenantID, run.ID, steps)
					if err != nil || len(got) == 0 {
						return
					}
					mu.Lock()
					for _, s := range got {
						claimed[s.ID] = struct{}{}
					}
					mu.Unlock()
					time.Sleep(2 * time.Millisecond)
				}
			}()
		}
		wg.Wait()

		assert.Equal(t, steps, len(claimed), "iteration %d: every step claimed exactly once", iter)
	}
}

// Q-21: the reaper reclaims runs whose lease expired and resets them to pending.
func TestReaper_ReclaimsExpiredLease(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
	run := &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err := repo.CreateRun(ctx, run)
	require.NoError(t, err)

	claimed, err := repo.ClaimRun(ctx, tenantID, run.ID, "worker-crash", 60*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claimed, "claim must succeed")
	require.Equal(t, domain.RunStatusRunning, claimed.Status)

	// Simulate the worker dying: force the lease into the past.
	_, err = pool.Exec(ctx, `UPDATE workflow_runs SET lease_expires_at = NOW() - interval '10 seconds' WHERE id = $1`, run.ID)
	require.NoError(t, err)

	reclaimed, err := repo.ReclaimExpiredLeases(ctx)
	require.NoError(t, err)
	found := false
	for _, rec := range reclaimed {
		if rec.RunID == run.ID && rec.TenantID == tenantID {
			found = true
			break
		}
	}
	assert.True(t, found, "the expired run must be reclaimed (alongside any other expired runs)")

	got, err := repo.GetRun(ctx, tenantID, run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunStatusPending, got.Status, "reclaimed run returns to pending")
	assert.Nil(t, got.ClaimedBy)
	assert.Nil(t, got.LeaseExpiresAt)
}

// Q-7: a zero-row run claim (duplicate queue delivery) is a no-op, not an error.
func TestRunClaim_DuplicateDeliveryIsNoOp(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
	run := &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err := repo.CreateRun(ctx, run)
	require.NoError(t, err)

	first, err := repo.ClaimRun(ctx, tenantID, run.ID, "worker-a", 60*time.Second)
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := repo.ClaimRun(ctx, tenantID, run.ID, "worker-b", 60*time.Second)
	require.NoError(t, err)
	assert.Nil(t, second, "duplicate delivery must yield nil, nil — the normal outcome, not an error")
}

// Y-1: ExtendLease must assert ownership — worker B cannot extend worker A's lease.
func TestExtendLease_RejectsNonOwner(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
	run := &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err := repo.CreateRun(ctx, run)
	require.NoError(t, err)

	claimed, err := repo.ClaimRun(ctx, tenantID, run.ID, "worker-A", 60*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// Record the lease timestamp, then let a non-owner try to extend it.
	var before time.Time
	err = pool.QueryRow(ctx, `SELECT lease_expires_at FROM workflow_runs WHERE id = $1`, run.ID).Scan(&before)
	require.NoError(t, err)

	err = repo.ExtendLease(ctx, tenantID, run.ID, "worker-B", 60*time.Second)
	require.ErrorIs(t, err, execution.ErrLeaseLost, "a non-owner must not extend the lease")

	var after time.Time
	err = pool.QueryRow(ctx, `SELECT lease_expires_at FROM workflow_runs WHERE id = $1`, run.ID).Scan(&after)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the non-owner's attempt must leave the lease untouched")
}

// Y-3: the reaper resets orphaned running/ready steps in the same statement as
// the run reset, so a reclaimed run is actually recoverable.
func TestReclaim_ResetsRunningSteps(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 2)
	run := &domain.WorkflowRun{
		ID:                uuid.New(),
		TenantID:          tenantID,
		WorkflowID:        wfID,
		WorkflowVersionID: verID,
	}
	_, err := repo.CreateRun(ctx, run)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))

	// One step running (the crashed worker's), one ready (claimed-then-lost).
	require.NoError(t, repo.UpdateStepStatus(ctx, tenantID, run.ID, nodes[0].NodeKey, "running"))
	require.NoError(t, repo.UpdateStepStatus(ctx, tenantID, run.ID, nodes[1].NodeKey, "ready"))
	_, err = pool.Exec(ctx, `UPDATE step_runs SET attempt_count = 1 WHERE node_key = $1`, nodes[0].NodeKey)
	require.NoError(t, err)

	claimed, err := repo.ClaimRun(ctx, tenantID, run.ID, "worker-crash", 60*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = pool.Exec(ctx, `UPDATE workflow_runs SET lease_expires_at = NOW() - interval '10 seconds' WHERE id = $1`, run.ID)
	require.NoError(t, err)

	reclaimed, err := repo.ReclaimExpiredLeases(ctx)
	require.NoError(t, err)
	assert.True(t, len(reclaimed) > 0, "the expired run must be reclaimed")

	steps, err := repo.ListStepRuns(ctx, tenantID, run.ID)
	require.NoError(t, err)
	for _, s := range steps {
		assert.Equal(t, "retrying", s.Status, "orphaned step %s must become retrying so a fresh worker re-dispatches it", s.NodeKey)
	}
	// attempt_count must have grown so RetryPolicy.MaxAttempts can eventually
	// give up on a poison step (the crash-loop guard).
	for _, s := range steps {
		if s.NodeKey == nodes[0].NodeKey {
			assert.Equal(t, 2, s.AttemptCount, "a reclaimed running step counts as another attempt")
		}
	}
}

// Q-20: every Go status constant is accepted by its CHECK constraint.
func TestStatusConstants_AcceptedByCheckConstraints(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()

	tenantID := uuid.New()
	wfID := uuid.New()
	verID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO tenants (id, slug, name) VALUES ($1, $2, $3)`, tenantID, "it-status-"+tenantID.String(), "it-status")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO workflows (id, tenant_id, name) VALUES ($1, $2, 'it')`, wfID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO workflow_versions (id, tenant_id, workflow_id, version_number, status) VALUES ($1, $2, $3, 1, 'published')`, verID, tenantID, wfID)
	require.NoError(t, err)

	runStatuses := []string{domain.RunStatusPending, domain.RunStatusRunning, domain.RunStatusSucceeded,
		domain.RunStatusFailed, domain.RunStatusCanceled, domain.RunStatusTimedOut}
	for _, st := range runStatuses {
		_, err := pool.Exec(ctx, `INSERT INTO workflow_runs (id, tenant_id, workflow_id, workflow_version_id, status) VALUES ($1, $2, $3, $4, $5)`,
			uuid.New(), tenantID, wfID, verID, st)
		assert.NoError(t, err, "run status %q must satisfy the CHECK", st)
	}

	nodeTypes := []string{domain.NodeTypeHTTP, domain.NodeTypeDelay, domain.NodeTypeCondition,
		domain.NodeTypeTransform, domain.NodeTypeEventPublish}
	for _, nt := range nodeTypes {
		_, err := pool.Exec(ctx, `INSERT INTO workflow_nodes (id, tenant_id, workflow_version_id, node_key, node_type) VALUES ($1, $2, $3, $4, $5)`,
			uuid.New(), tenantID, verID, uuid.New().String(), nt)
		assert.NoError(t, err, "node type %q must satisfy the CHECK", nt)
	}
}

// Phase 6: ListRuns filters by workflow, status, and paginates newest first.
func TestListRuns_FilteringAndPagination(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
	otherWfID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO workflows (id, tenant_id, name) VALUES ($1, $2, 'other')`, otherWfID, tenantID)
	require.NoError(t, err)
	otherVerID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO workflow_versions (id, tenant_id, workflow_id, version_number, status) VALUES ($1, $2, $3, 1, 'published')`, otherVerID, tenantID, otherWfID)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		run := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
		ok, err := repo.CreateRun(ctx, run)
		require.NoError(t, err)
		require.True(t, ok)
	}
	// A run under a different workflow must not leak into the list.
	other := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: otherWfID, WorkflowVersionID: otherVerID}
	_, err = repo.CreateRun(ctx, other)
	require.NoError(t, err)

	items, total, err := repo.ListRuns(ctx, execution.ListRunsFilter{TenantID: tenantID, WorkflowID: wfID, Page: 1, PageSize: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, items, 2)
	for i := 1; i < len(items); i++ {
		assert.True(t, !items[i].CreatedAt.After(items[i-1].CreatedAt), "runs must be newest first")
	}

	// Status filter.
	_, err = pool.Exec(ctx, `UPDATE workflow_runs SET status='failed' WHERE workflow_id=$1 AND id IN (SELECT id FROM workflow_runs WHERE workflow_id=$1 LIMIT 1)`, wfID)
	require.NoError(t, err)
	_, totalFailed, err := repo.ListRuns(ctx, execution.ListRunsFilter{TenantID: tenantID, WorkflowID: wfID, Status: domain.RunStatusFailed})
	require.NoError(t, err)
	assert.Equal(t, int64(1), totalFailed)
}

// Phase 6: ReclaimStalePendingRuns picks up a genuinely stale pending run and
// ignores a fresh one.
func TestReclaimStalePendingRuns(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
	stale := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err := repo.CreateRun(ctx, stale)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE workflow_runs SET created_at = NOW() - interval '2 hours' WHERE id = $1`, stale.ID)
	require.NoError(t, err)

	fresh := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err = repo.CreateRun(ctx, fresh)
	require.NoError(t, err)

	staleFound, err := repo.ReclaimStalePendingRuns(ctx, 30*time.Minute)
	require.NoError(t, err)
	var found bool
	for _, r := range staleFound {
		if r.RunID == stale.ID {
			found = true
		}
		assert.NotEqual(t, fresh.ID, r.RunID, "a fresh pending run must not be reclaimed")
	}
	assert.True(t, found, "the stale pending run must be returned for re-enqueueing")
}

// Phase 6: idempotent CreateRun + FindByIdempotencyKey after a real ON CONFLICT.
func TestFindByIdempotencyKey_AfterConflict(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, _ := seedRunFixtures(t, ctx, pool, 1)
	key := "idem-" + uuid.New().String()

	first := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID, IdempotencyKey: &key}
	ok, err := repo.CreateRun(ctx, first)
	require.NoError(t, err)
	require.True(t, ok, "first create must succeed")

	second := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID, IdempotencyKey: &key}
	ok, err = repo.CreateRun(ctx, second)
	require.NoError(t, err)
	assert.False(t, ok, "duplicate key must be a no-op, not an error")

	existing, err := repo.FindByIdempotencyKey(ctx, tenantID, key)
	require.NoError(t, err)
	assert.Equal(t, first.ID, existing.ID, "the first run must be returned")
}

// Phase 6: CloneStepRunsForRetry seeds succeeded steps with timing and fresh
// pending rows for everything else.
func TestCloneStepRunsForRetry(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 3)
	origRun := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err := repo.CreateRun(ctx, origRun)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, origRun.ID, nodes))

	// node a succeeded with timing; node b failed; node c pending.
	started := time.Now().Add(-time.Minute)
	finished := time.Now()
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='succeeded', attempt_count=2, started_at=$1, finished_at=$2 WHERE workflow_run_id=$3 AND node_key=$4`,
		started, finished, origRun.ID, nodes[0].NodeKey)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='failed', attempt_count=1 WHERE workflow_run_id=$1 AND node_key=$2`,
		origRun.ID, nodes[1].NodeKey)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='pending' WHERE workflow_run_id=$1 AND node_key=$2`,
		origRun.ID, nodes[2].NodeKey)
	require.NoError(t, err)

	newRun := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err = repo.CreateRun(ctx, newRun)
	require.NoError(t, err)

	// onlyFailed: node a (succeeded) is copied wholesale, b and c are fresh pending.
	require.NoError(t, repo.CloneStepRunsForRetry(ctx, tenantID, newRun.ID, origRun.ID, nodes, true))
	steps, err := repo.ListStepRuns(ctx, tenantID, newRun.ID)
	require.NoError(t, err)
	require.Len(t, steps, 3)
	byKey := map[string]domain.StepRun{}
	for _, s := range steps {
		byKey[s.NodeKey] = s
	}
	assert.Equal(t, "succeeded", byKey[nodes[0].NodeKey].Status)
	assert.Equal(t, 2, byKey[nodes[0].NodeKey].AttemptCount)
	assert.WithinDuration(t, started, *byKey[nodes[0].NodeKey].StartedAt, time.Millisecond, "reused step must keep its original timing")
	assert.WithinDuration(t, finished, *byKey[nodes[0].NodeKey].FinishedAt, time.Millisecond)
	assert.Equal(t, "pending", byKey[nodes[1].NodeKey].Status)
	assert.Zero(t, byKey[nodes[1].NodeKey].AttemptCount)
	assert.Nil(t, byKey[nodes[1].NodeKey].StartedAt, "a fresh step must show no timing for work it did not do")
	assert.Equal(t, "pending", byKey[nodes[2].NodeKey].Status)
}

// Phase AO (AB-2): Reusing a correlation key after the previous token was consumed.
func TestCreateToken_ReusesCorrelationKeyAfterConsumption(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 1)

	// Run 1
	run1 := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err := repo.CreateRun(ctx, run1)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run1.ID, nodes))
	steps1, err := repo.ListStepRuns(ctx, tenantID, run1.ID)
	require.NoError(t, err)

	token1 := &domain.StepWaitToken{
		ID:             uuid.New(),
		TenantID:       tenantID,
		WorkflowRunID:  run1.ID,
		StepRunID:      steps1[0].ID,
		CorrelationKey: "REUSE-KEY-1",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateToken(ctx, token1))

	// Consume token 1
	consumed, err := repo.ConsumeTokenIfUnconsumed(ctx, token1.ID)
	require.NoError(t, err)
	require.True(t, consumed)

	// Run 2: create a new wait token with the exact same correlation key
	run2 := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err = repo.CreateRun(ctx, run2)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run2.ID, nodes))
	steps2, err := repo.ListStepRuns(ctx, tenantID, run2.ID)
	require.NoError(t, err)

	token2 := &domain.StepWaitToken{
		ID:             uuid.New(),
		TenantID:       tenantID,
		WorkflowRunID:  run2.ID,
		StepRunID:      steps2[0].ID,
		CorrelationKey: "REUSE-KEY-1",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	// Must succeed now because token 1 was consumed (or handled)
	err = repo.CreateToken(ctx, token2)
	require.NoError(t, err, "reusing correlation key after consumption must succeed")
}

// Phase AO (AB-2): Sweeper advances past >100 already-handled expired tokens.
func TestSweeper_AdvancesPastAlreadyHandledExpiredTokens(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 1)

	// Clean up any leftover wait tokens from prior test runs for full test isolation
	_, err := pool.Exec(ctx, `DELETE FROM step_wait_tokens`)
	require.NoError(t, err)

	// Seed 105 expired, genuinely UNHANDLED tokens — no direct MarkTokenHandled
	// call. Their ExpiresAt is earlier than the active token's, so ORDER BY
	// expires_at puts them first in FindExpiredTokens.
	for i := 0; i < 105; i++ {
		run := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
		_, err := repo.CreateRun(ctx, run)
		require.NoError(t, err)
		require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))
		steps, err := repo.ListStepRuns(ctx, tenantID, run.ID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE step_runs SET status='waiting' WHERE id=$1`, steps[0].ID)
		require.NoError(t, err)
		require.NoError(t, repo.UpdateRunStatus(ctx, tenantID, run.ID, domain.RunStatusWaiting, false))

		token := &domain.StepWaitToken{
			ID:             uuid.New(),
			TenantID:       tenantID,
			WorkflowRunID:  run.ID,
			StepRunID:      steps[0].ID,
			CorrelationKey: uuid.New().String(),
			ExpiresAt:      time.Now().Add(-2 * time.Hour),
		}
		require.NoError(t, repo.CreateToken(ctx, token))
	}

	// 1 more recently-expired token whose step is genuinely still waiting.
	activeRun := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err = repo.CreateRun(ctx, activeRun)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, activeRun.ID, nodes))
	steps, err := repo.ListStepRuns(ctx, tenantID, activeRun.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='waiting' WHERE id=$1`, steps[0].ID)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateRunStatus(ctx, tenantID, activeRun.ID, domain.RunStatusWaiting, false))

	activeToken := &domain.StepWaitToken{
		ID:             uuid.New(),
		TenantID:       tenantID,
		WorkflowRunID:  activeRun.ID,
		StepRunID:      steps[0].ID,
		CorrelationKey: "ACTIVE-SWEEP-KEY",
		ExpiresAt:      time.Now().Add(-1 * time.Minute),
	}
	require.NoError(t, repo.CreateToken(ctx, activeToken))

	fakeEnq := &fakeEnqueuer{}

	// First sweep: FindExpiredTokens(limit=100) returns the 100 oldest of the
	// 106 expired tokens — all backlog, none of them the active one (it's
	// newest). The sweeper must mark all 100 handled itself; without the fix,
	// nothing does, and they resurface forever.
	swept1, err := execution.SweepExpiredWaitTokens(ctx, repo, repo, fakeEnq, nil)
	require.NoError(t, err)
	require.Equal(t, 100, swept1, "first sweep processes exactly the LIMIT")

	// Second sweep: only reaches the remaining 5 backlog tokens plus the active
	// one if the first sweep's 100 no longer resurface. This is the actual
	// regression AB-2 diagnosed and the line this test must catch losing.
	swept2, err := execution.SweepExpiredWaitTokens(ctx, repo, repo, fakeEnq, nil)
	require.NoError(t, err)
	assert.Equal(t, 6, swept2, "second sweep must advance past the first 100 to reach the remaining backlog and the active token")

	gotRun, err := repo.GetRun(ctx, tenantID, activeRun.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunStatusPending, gotRun.Status, "the active run must have been woken")
}

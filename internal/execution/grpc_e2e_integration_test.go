package execution_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/eventbus"
	"flowforge/internal/platform/eventbus/eventspb"
	"flowforge/internal/platform/postgres"
	"flowforge/internal/workflow"
)

// Part C E2E: a workflow run's EVENT_PUBLISH(grpc) delivers to the gRPC ingress
// server, which resolves a real EVENT_WAIT parked in a second run — proving the
// egress client and ingress server are wire-compatible by construction.
func TestGRPC_E2EPublishResolvesWait(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)
	uow := postgres.NewUnitOfWork(pool)

	// A real gRPC listener on a free port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port

	secret := "grpc-e2e-secret"
	eventService := execution.NewEventService(repo, repo, &fakeEnqueuer{}, uow, slog.Default())
	secretGetter := func(ctx context.Context, tenantID uuid.UUID) (string, error) {
		return repo.GetWebhookSecret(ctx, tenantID)
	}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(eventbus.AuthInterceptor(secretGetter)))
	eventspb.RegisterEventListenerServer(grpcSrv, eventbus.NewEventListenerServer(eventService))
	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()
	select {
	case err := <-serveErr:
		t.Fatalf("grpc server failed to serve: %v", err)
	default:
	}
	// Confirm the listener is actually reachable before publishing.
	probe, perr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if perr != nil {
		t.Fatalf("listener not reachable on port %d: %v", port, perr)
	}
	probe.Close()

	// Run B: a workflow with an EVENT_WAIT node, parked on a token.
	tenantID, wfB, verB, nodesB := seedRunFixtures(t, ctx, pool, 1)
	require.NoError(t, repo.SetWebhookSecret(ctx, tenantID, secret))
	runB := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfB, WorkflowVersionID: verB}
	_, err = repo.CreateRun(ctx, runB)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, runB.ID, nodesB))
	require.NoError(t, repo.UpdateRunStatus(ctx, tenantID, runB.ID, domain.RunStatusWaiting, false))
	stepsB, err := repo.ListStepRuns(ctx, tenantID, runB.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='waiting' WHERE id=$1`, stepsB[0].ID)
	require.NoError(t, err)
	token := &domain.StepWaitToken{
		ID: uuid.New(), TenantID: tenantID, WorkflowRunID: runB.ID, StepRunID: stepsB[0].ID,
		CorrelationKey: "E2E-KEY", ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateToken(ctx, token))

	// Run A: a workflow (in the SAME tenant as run B) with an EVENT_PUBLISH(grpc,
	// key E2E-KEY) driven by the coordinator through the Router.
	wfA := uuid.New()
	verA := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO workflows (id, tenant_id, name) VALUES ($1,$2,'publisher')`, wfA, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO workflow_versions (id, tenant_id, workflow_id, version_number, status) VALUES ($1,$2,$3,1,'published')`, verA, tenantID, wfA)
	require.NoError(t, err)
	nodeA := uuid.New()
	cfgBytes, _ := json.Marshal(map[string]any{
		"eventType": "flowforge.internal.test", "correlationKey": "E2E-KEY",
		"payload": `{"correlationKey":"E2E-KEY"}`, "transport": "grpc",
		"target": "127.0.0.1:" + strconv.Itoa(port),
	})
	_, err = pool.Exec(ctx, `INSERT INTO workflow_nodes (id, tenant_id, workflow_version_id, node_key, node_type, config) VALUES ($1,$2,$3,'pub','EVENT_PUBLISH',$4)`,
		nodeA, tenantID, verA, string(cfgBytes))
	require.NoError(t, err)

	runA := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfA, WorkflowVersionID: verA}
	_, err = repo.CreateRun(ctx, runA)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, runA.ID, []domain.WorkflowNode{{
		ID: nodeA, TenantID: tenantID, WorkflowVersionID: verA, NodeKey: "pub", NodeType: domain.NodeTypeEventPublish,
	}}))

	router := eventbus.Router{
		GRPC: eventbus.NewGRPCPublisher(secretGetter),
	}
	coord := execution.NewCoordinator(repo, repo, repo, workflow.NewVersionRepository(pool),
		executor.NewRegistry(nil, 1<<20, nil),
		execution.CoordinatorConfig{
			Concurrency: 4, Lease: 60 * time.Second, Retry: engine.DefaultRetryPolicy(),
			Timeout: engine.DefaultTimeoutPolicy(), WorkerID: "it-grpc", Logger: slog.New(slog.DiscardHandler),
			Publisher: router,
		})
	require.NoError(t, coord.HandleRun(ctx, tenantID, runA.ID))

	// The published event must have resolved run B's token.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := repo.FindTokenByCorrelationKey(ctx, tenantID, "E2E-KEY")
		require.NoError(t, err)
		if got.ConsumedAt != nil {
			assert.True(t, true)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("EVENT_PUBLISH(grpc) never resolved the waiting run's token")
}

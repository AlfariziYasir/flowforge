package execution_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/execution"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/eventbus"
	"flowforge/internal/platform/postgres"
)

// Phase 7 (Part D): a message published to real NATS/JetStream is consumed,
// signature-verified, and resolves the matching wait token.
func TestNATS_EventResolvesWaitToken(t *testing.T) {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Skipf("nats not reachable at %s: %v", natsURL, err)
	}
	defer nc.Close()

	pool := getTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := execution.NewExecutionRepository(pool)

	tenantID, wfID, verID, nodes := seedRunFixtures(t, ctx, pool, 1)
	run := &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, WorkflowID: wfID, WorkflowVersionID: verID}
	_, err = repo.CreateRun(ctx, run)
	require.NoError(t, err)
	require.NoError(t, repo.CreateStepRuns(ctx, tenantID, run.ID, nodes))

	// Park the run and step on a wait token.
	secret := "nats-it-secret"
	require.NoError(t, repo.SetWebhookSecret(ctx, tenantID, secret))
	require.NoError(t, repo.UpdateRunStatus(ctx, tenantID, run.ID, domain.RunStatusWaiting, false))
	steps, err := repo.ListStepRuns(ctx, tenantID, run.ID)
	require.NoError(t, err)
	require.Len(t, steps, 1)

	// The step must be 'waiting' for HandleEvent's gated success transition.
	_, err = pool.Exec(ctx, `UPDATE step_runs SET status='waiting' WHERE id=$1`, steps[0].ID)
	require.NoError(t, err)
	token := &domain.StepWaitToken{
		ID:             uuid.New(),
		TenantID:       tenantID,
		WorkflowRunID:  run.ID,
		StepRunID:      steps[0].ID,
		CorrelationKey: "NATS-ORD-1",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.CreateToken(ctx, token))

	js, err := nc.JetStream()
	require.NoError(t, err)
	// Purge leftover test streams from prior runs (the fixed wildcard stream would
	// otherwise overlap any per-run subject).
	for info := range js.StreamsInfo() {
		if strings.HasPrefix(info.Config.Name, "FLOWFORGE_EVENTS_IT") {
			_ = js.DeleteStream(info.Config.Name)
		}
	}
	// Per-run unique subject + stream so repeated runs never collide on subjects.
	suffix := uuid.New().String()[:8]
	subject := "flowforge.events.it." + suffix
	require.NoError(t, eventbus.EnsureStream(js, "FLOWFORGE_EVENTS_IT_"+suffix, subject))

	eventService := execution.NewEventService(repo, repo, &fakeEnqueuer{}, postgres.NewUnitOfWork(pool), slog.Default())
	secretGetter := func(ctx context.Context, tenantID uuid.UUID) (string, error) {
		return repo.GetWebhookSecret(ctx, tenantID)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- eventbus.Subscribe(subCtx, nc, subject, "it-"+uuid.New().String()[:8], secretGetter, eventService, slog.Default())
	}()

	// Publish a signed event the subscriber will verify and resolve.
	payload := []byte(`{"correlationKey":"NATS-ORD-1","paid":true}`)
	pub := eventbus.NewNATSPublisher(js, secretGetter)
	require.NoError(t, pub.Publish(ctx, executor.PublishInput{
		EventType:      "payment.paid",
		CorrelationKey: "NATS-ORD-1",
		TenantID:       tenantID,
		Transport:      "nats",
		Target:         subject,
		Payload:        payload,
	}))

	// Poll until the token is consumed.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := repo.FindTokenByCorrelationKey(ctx, tenantID, "NATS-ORD-1")
		require.NoError(t, err)
		if got.ConsumedAt != nil {
			assert.True(t, true)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("wait token was never consumed via NATS")
}

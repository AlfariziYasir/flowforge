package eventstream_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/platform/eventstream"
)

func getTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	if os.Getenv("FLOWFORGE_INTEGRATION") != "1" {
		t.Skip("Skipping Redis integration test; FLOWFORGE_INTEGRATION != 1")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}

	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Cannot connect to Redis at %s: %v", redisURL, err)
	}

	return client
}

func TestNoopPublisherAndClientManager(t *testing.T) {
	pub := eventstream.NewNoopPublisher()
	err := pub.Publish(context.Background(), domain.Event{Type: domain.EventHeartbeat})
	assert.NoError(t, err)

	mgr := eventstream.NewNoopClientManager()
	connID, _, unreg := mgr.Register(uuid.New())
	assert.NotEmpty(t, connID)
	unreg()
	assert.NoError(t, mgr.Close())
}

func TestRedisPublisherAndClientManager_Integration(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()

	ctx := context.Background()
	publisher := eventstream.NewRedisPublisher(rdb)
	manager := eventstream.NewClientManager(rdb)
	defer func() { _ = manager.Close() }()

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Register 2 clients for Tenant A and 1 client for Tenant B
	connA1, chA1, unregA1 := manager.Register(tenantA)
	defer unregA1()
	assert.NotEmpty(t, connA1)

	connA2, chA2, unregA2 := manager.Register(tenantA)
	defer unregA2()
	assert.NotEmpty(t, connA2)

	connB1, chB1, unregB1 := manager.Register(tenantB)
	defer unregB1()
	assert.NotEmpty(t, connB1)

	// Allow subscription propagation in Redis
	time.Sleep(100 * time.Millisecond)

	// Publish Event to Tenant A
	runID := uuid.New()
	eventA := domain.Event{
		Type:      domain.EventRunStarted,
		TenantID:  tenantA,
		RunID:     &runID,
		Status:    "running",
		Timestamp: time.Now().UTC(),
	}
	err := publisher.Publish(ctx, eventA)
	require.NoError(t, err)

	// Both Client A1 and Client A2 must receive eventA
	select {
	case received := <-chA1:
		assert.Equal(t, domain.EventRunStarted, received.Type)
		assert.Equal(t, tenantA, received.TenantID)
		assert.Equal(t, runID, *received.RunID)
	case <-time.After(2 * time.Second):
		t.Fatal("Client A1 timed out waiting for event")
	}

	select {
	case received := <-chA2:
		assert.Equal(t, domain.EventRunStarted, received.Type)
		assert.Equal(t, tenantA, received.TenantID)
	case <-time.After(2 * time.Second):
		t.Fatal("Client A2 timed out waiting for event")
	}

	// Client B1 must NOT receive Tenant A's event (Tenant Isolation)
	select {
	case received := <-chB1:
		t.Fatalf("Client B1 received unexpected event from Tenant A: %+v", received)
	case <-time.After(200 * time.Millisecond):
		// Expected: no event
	}

	// Unregister Client A1
	unregA1()

	// Publish second event to Tenant A
	eventA2 := domain.Event{
		Type:      domain.EventRunCompleted,
		TenantID:  tenantA,
		RunID:     &runID,
		Status:    "succeeded",
		Timestamp: time.Now().UTC(),
	}
	err = publisher.Publish(ctx, eventA2)
	require.NoError(t, err)

	// Client A2 must receive eventA2
	select {
	case received := <-chA2:
		assert.Equal(t, domain.EventRunCompleted, received.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("Client A2 timed out waiting for eventA2")
	}
}

// AD-3: Concurrent Register and Unregister calls tear down Redis subscriptions cleanly without race conditions.
func TestClientManager_ConcurrentRegisterUnregister(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()
	mgr := eventstream.NewClientManager(rdb, nil)
	defer func() { _ = mgr.Close() }()

	tenantID := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, unreg := mgr.Register(tenantID)
			unreg()
			unreg() // double-unregister must be safe (sync.Once)
		}()
	}
	wg.Wait()

	// Prove the subscription tore down cleanly and can be re-established:
	// register fresh and confirm a published event is actually received.
	_, events, unreg := mgr.Register(tenantID)
	defer unreg()
	time.Sleep(100 * time.Millisecond)

	pub := eventstream.NewRedisPublisher(rdb)
	require.NoError(t, pub.Publish(context.Background(), domain.Event{Type: domain.EventHeartbeat, TenantID: tenantID}))

	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("post-storm registration never received an event — subscription left in a broken state")
	}
}

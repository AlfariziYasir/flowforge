// Package queue wraps Asynq as FlowForge's operational queue.
//
// The queue carries ONLY a run ID and tenant ID (D-3): never the graph, never
// payload data, so Redis holds no tenant content. Postgres is the source of
// truth for claims; Redis only wakes workers (D-4). A duplicate queue delivery
// is harmless because the DB claim fails, not because the queue promised
// exactly-once.
package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	redisclient "github.com/redis/go-redis/v9"

	"flowforge/internal/execution/executor"
)

// RunQueue is the asynq queue that run-execution tasks land on.
const RunQueue = "workflow"

// TaskType for a workflow-run execution wake-up.
const TaskTypeRun = "workflow:run"

// RunPayload is the entire message: two UUIDs.
type RunPayload struct {
	TenantID uuid.UUID `json:"tenant_id"`
	RunID    uuid.UUID `json:"run_id"`
}

// Client enqueues run wake-up tasks and inspects queue depth.
type Client struct {
	client    *asynq.Client
	inspector *asynq.Inspector
}

// NewClient builds a queue client from a go-redis client (shares its pool).
func NewClient(rc *redisclient.Client) *Client {
	opt := asynq.RedisClientOpt{Addr: rc.Options().Addr, Password: rc.Options().Password, DB: rc.Options().DB}
	return &Client{client: asynq.NewClient(opt), inspector: asynq.NewInspector(opt)}
}

// EnqueueRun schedules a run for pickup. Callers must not treat failure as data
// loss: the worker re-reads authoritative state from Postgres regardless.
func (c *Client) EnqueueRun(tenantID, runID uuid.UUID) error {
	payload, err := json.Marshal(RunPayload{TenantID: tenantID, RunID: runID})
	if err != nil {
		return fmt.Errorf("marshal run payload: %w", err)
	}
	_, err = c.client.Enqueue(asynq.NewTask(TaskTypeRun, payload), asynq.Queue(RunQueue))
	if err != nil {
		return fmt.Errorf("enqueue run: %w", err)
	}
	return nil
}

// QueueDepth reports the number of pending run tasks.
func (c *Client) QueueDepth() (int64, error) {
	info, err := c.inspector.GetQueueInfo(RunQueue)
	if err != nil {
		return 0, err
	}
	return int64(info.Pending), nil
}

// Close shuts down the enqueuer and inspector.
func (c *Client) Close() error {
	c.inspector.Close()
	return c.client.Close()
}

// Publish delivers an event message onto the internal queue, satisfying
// executor.EventPublisher. Phase 7's Router uses this as the default transport
// (transport==""), preserving Phase 5's EVENT_PUBLISH behavior byte-identically.
// It is also the eventual Phase 7 outbox's relay sink.
func (c *Client) Publish(ctx context.Context, in executor.PublishInput) error {
	if in.EventType == "" {
		return fmt.Errorf("publish event: empty event type")
	}
	_, err := c.client.Enqueue(asynq.NewTask("workflow:event:"+in.EventType, in.Payload), asynq.Queue(RunQueue))
	if err != nil {
		return fmt.Errorf("publish event: %w", err)
	}
	return nil
}

// NewServer builds an asynq server draining RunQueue. The handler owns the run:
// it must claim the run in Postgres before doing work; a duplicate wake-up is a
// no-op because the claim fails. ctx carries asynq's cancellation (drained on
// Shutdown) and must be respected by the handler.
func NewServer(rc *redisclient.Client, concurrency int, runHandler func(ctx context.Context, tenantID, runID uuid.UUID) error) (*asynq.Server, asynq.Handler) {
	srv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: rc.Options().Addr, Password: rc.Options().Password, DB: rc.Options().DB},
		asynq.Config{
			Concurrency: concurrency,
			Queues:      map[string]int{RunQueue: 1},
		},
	)

	handler := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		var p RunPayload
		if err := json.Unmarshal(task.Payload(), &p); err != nil {
			return fmt.Errorf("malformed %s payload: %w", task.Type(), err)
		}
		return runHandler(ctx, p.TenantID, p.RunID)
	})

	return srv, handler
}

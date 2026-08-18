// Package eventbus implements the Phase 7 multi-protocol event adapters: gRPC
// and NATS ingress/egress, plus the Router that dispatches EVENT_PUBLISH to a
// transport per node config.
//
// D-4: the wire contract here (proto messages, NATS subject naming, JetStream
// stream config) is a working, generic placeholder pending the real external
// system's actual integration spec. Adapting field names/auth is isolated to
// this package — it does not reopen the wait-token mechanism, the coordinator,
// or the migration.
package eventbus

import (
	"context"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/execution"
)

// EventHandler is the ingress adapters' port: the same HandleEvent every
// transport calls, plus orphan dead-lettering.
type EventHandler interface {
	HandleEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) (resolved bool, err error)
	RecordOrphanEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte, reason string) error
}

// RunTriggerer is the port for triggering workflow runs from queue/gRPC ingress (Case A).
type RunTriggerer interface {
	CreateRun(ctx context.Context, cmd execution.CreateRunCommand) (*domain.WorkflowRun, error)
}

// SecretGetter resolves a tenant's event-ingress signing secret. The concrete
// wiring passes through to the execution repository.
type SecretGetter func(ctx context.Context, tenantID uuid.UUID) (string, error)

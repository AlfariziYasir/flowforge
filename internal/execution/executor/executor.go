// Package executor implements the five step node types (HTTP, DELAY, CONDITION,
// TRANSFORM, EVENT_PUBLISH). Executors do I/O by definition, so they live in
// internal/execution — never in internal/engine, whose purity is mechanically
// enforced. The coordinator applies timeouts; an executor must never assume one.
package executor

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
)

// ErrUnknownNodeType is returned when a graph node has a type no executor serves.
var ErrUnknownNodeType = errors.New("unknown node type")

// Input is everything an executor may see: the node definition, the engine scope
// (interpolate/evaluate against), and the injected I/O sinks.
type Input struct {
	Node         domain.NodeInput
	Scope        engine.Scope
	HTTP         *http.Client   // SSRF-pinned client; nil-safe (only HTTP uses it)
	MaxBodyBytes int64          // response body cap for HTTP
	Publisher    EventPublisher // EVENT_PUBLISH sink; nil-safe

	// Run/step identity for executors that persist cross-table state (EVENT_WAIT).
	TenantID  uuid.UUID
	RunID     uuid.UUID
	StepRunID uuid.UUID

	// WaitTokens is EVENT_WAIT's sink for creating wait tokens. nil-safe.
	WaitTokens WaitTokenStore
	// WaitConfig carries the per-tenant cap and expiry bounds for wait tokens.
	WaitConfig WaitConfig
}

// Output is the step's recorded result, written verbatim to output_payload.
type Output struct {
	Data map[string]any
	// Waiting marks that the step parked itself on a wait token: the coordinator
	// must move both the step and the run to 'waiting' and release the run.
	Waiting bool
}

// WaitTokenStore is what EVENT_WAIT needs to park a run: create a token and
// count a tenant's active tokens for the per-tenant cap.
type WaitTokenStore interface {
	CreateToken(ctx context.Context, token domain.StepWaitToken) error
	CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error)
}

// WaitConfig bounds wait tokens: a tenant cannot exceed MaxPerTenant active
// waits, and a token expires after DefaultDuration (capped at MaxDuration).
type WaitConfig struct {
	MaxPerTenant    int
	DefaultDuration time.Duration
	MaxDuration     time.Duration
}

// PublishInput is everything an EVENT_PUBLISH node delivers: the message plus
// the transport context that selects and addresses the egress adapter.
type PublishInput struct {
	EventType      string
	CorrelationKey string
	Transport      string // "" (internal queue) | "grpc" | "nats"
	Target         string // grpc: "host:port"; nats: subject; ignored when Transport == ""
	TenantID       uuid.UUID
	Payload        []byte
}

// EventPublisher delivers an event message out of the platform.
type EventPublisher interface {
	Publish(ctx context.Context, in PublishInput) error
}

// Executor runs a single node type. ctx carries the coordinator-applied timeout
// and must be respected on cancellation; no executor may use a bare time.Sleep.
type Executor interface {
	Type() string
	Execute(ctx context.Context, in Input) (Output, error)
}

// Registry maps node types to executors.
type Registry struct {
	byType map[string]Executor
}

// NewRegistry wires the executors around shared sinks.
func NewRegistry(httpClient *http.Client, maxBodyBytes int64, publisher EventPublisher) *Registry {
	return &Registry{byType: map[string]Executor{
		domain.NodeTypeHTTP:         &HTTP{},
		domain.NodeTypeDelay:        &Delay{},
		domain.NodeTypeCondition:    &Condition{},
		domain.NodeTypeTransform:    &Transform{},
		domain.NodeTypeEventPublish: &EventPublish{},
		domain.NodeTypeEventWait:    &EventWait{},
	}}
}

// Get returns the executor for nodeType, or (nil, false).
func (r *Registry) Get(nodeType string) (Executor, bool) {
	e, ok := r.byType[nodeType]
	return e, ok
}

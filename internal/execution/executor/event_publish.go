package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"flowforge/internal/engine"
)

// eventPublishConfig is the EVENT_PUBLISH node's config shape.
type eventPublishConfig struct {
	EventType      string `json:"eventType"`
	Payload        string `json:"payload"`
	CorrelationKey string `json:"correlationKey"`
	Transport      string `json:"transport"` // "" (internal queue, default) | "grpc" | "nats"
	Target         string `json:"target"`    // grpc: "host:port"; nats: subject; ignored when transport == ""
}

// EventPublish delivers a message through the injected publisher. Its shape
// mirrors HTTP: interpolate from scope, then hand off to the I/O sink. The
// transport/target fields select which egress adapter runs (via the Router);
// "" keeps Phase 5's internal-queue behavior byte-identical.
type EventPublish struct{}

func (EventPublish) Type() string { return "EVENT_PUBLISH" }

func (EventPublish) Execute(ctx context.Context, in Input) (Output, error) {
	var cfg eventPublishConfig
	if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
		return Output{}, fmt.Errorf("event_publish: invalid config: %w", err)
	}
	if cfg.EventType == "" {
		return Output{}, fmt.Errorf("event_publish: eventType is empty")
	}
	if in.Publisher == nil {
		return Output{}, fmt.Errorf("event_publish: no publisher configured")
	}

	payload, err := engine.Interpolate(cfg.Payload, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("event_publish: payload: %w", err)
	}
	corr, err := engine.Interpolate(cfg.CorrelationKey, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("event_publish: correlationKey: %w", err)
	}

	if err := in.Publisher.Publish(ctx, PublishInput{
		EventType:      cfg.EventType,
		CorrelationKey: corr,
		Transport:      cfg.Transport,
		Target:         cfg.Target,
		TenantID:       in.TenantID,
		Payload:        []byte(payload),
	}); err != nil {
		return Output{}, fmt.Errorf("event_publish: %w", err)
	}
	return Output{Data: map[string]any{
		"publishedEventType": cfg.EventType,
		"correlationKey":     corr,
		"transport":          cfg.Transport,
	}}, nil
}

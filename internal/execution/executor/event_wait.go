package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
)

// eventWaitConfig is the EVENT_WAIT node's config shape.
type eventWaitConfig struct {
	// CorrelationKey is interpolated from scope; it is what the inbound event
	// carries to wake this run.
	CorrelationKey string `json:"correlationKey"`
	// WaitSeconds overrides the default token expiry. 0/absent uses the
	// coordinator's DefaultWaitDuration.
	WaitSeconds int `json:"waitSeconds"`
}

// EventWait parks the run and step on a wait token and returns Waiting=true, so
// the coordinator releases the run instead of holding a goroutine for what may
// be hours. The inbound event (any transport) later resolves the token.
type EventWait struct{}

func (EventWait) Type() string { return "EVENT_WAIT" }

func (EventWait) Execute(ctx context.Context, in Input) (Output, error) {
	if in.WaitTokens == nil {
		return Output{}, fmt.Errorf("event_wait: no wait-token store configured")
	}
	if in.TenantID == uuid.Nil {
		return Output{}, fmt.Errorf("event_wait: missing tenant identity")
	}

	var cfg eventWaitConfig
	if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
		return Output{}, fmt.Errorf("event_wait: invalid config: %w", err)
	}

	key, err := engine.Interpolate(cfg.CorrelationKey, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("event_wait: correlationKey: %w", err)
	}
	if key == "" {
		return Output{}, fmt.Errorf("event_wait: correlationKey resolved to empty")
	}

	// Per-tenant cap: one tenant must not fill the token table and starve others.
	if in.WaitConfig.MaxPerTenant > 0 {
		active, err := in.WaitTokens.CountActiveTokens(ctx, in.TenantID)
		if err != nil {
			return Output{}, fmt.Errorf("event_wait: count active tokens: %w", err)
		}
		if active >= in.WaitConfig.MaxPerTenant {
			return Output{}, fmt.Errorf("event_wait: tenant %s already has %d active wait tokens (cap %d)",
				in.TenantID, active, in.WaitConfig.MaxPerTenant)
		}
	}

	duration := time.Duration(cfg.WaitSeconds) * time.Second
	if duration <= 0 {
		duration = in.WaitConfig.DefaultDuration
	}
	if in.WaitConfig.MaxDuration > 0 && duration > in.WaitConfig.MaxDuration {
		duration = in.WaitConfig.MaxDuration
	}
	if duration <= 0 {
		duration = 24 * time.Hour // last-resort floor; config should set this
	}

	token := domain.StepWaitToken{
		ID:             uuid.New(),
		TenantID:       in.TenantID,
		WorkflowRunID:  in.RunID,
		StepRunID:      in.StepRunID,
		CorrelationKey: key,
		ExpiresAt:      time.Now().Add(duration),
	}
	if err := in.WaitTokens.CreateToken(ctx, token); err != nil {
		return Output{}, fmt.Errorf("event_wait: create token: %w", err)
	}

	return Output{Waiting: true, Data: map[string]any{
		"correlationKey": key,
		"expiresAt":      token.ExpiresAt,
	}}, nil
}

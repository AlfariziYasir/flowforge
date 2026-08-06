package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// delayConfig is the DELAY node's config shape.
type delayConfig struct {
	Seconds int `json:"seconds"`
}

// Delay sleeps for the configured duration, honouring cancellation: a cancelled
// context returns immediately instead of waiting out the full delay.
type Delay struct{}

func (Delay) Type() string { return "DELAY" }

func (Delay) Execute(ctx context.Context, in Input) (Output, error) {
	var cfg delayConfig
	if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
		return Output{}, fmt.Errorf("delay: invalid config: %w", err)
	}
	if cfg.Seconds < 0 {
		return Output{}, fmt.Errorf("delay: seconds must not be negative")
	}

	select {
	case <-time.After(time.Duration(cfg.Seconds) * time.Second):
		return Output{Data: map[string]any{"waitedSeconds": cfg.Seconds}}, nil
	case <-ctx.Done():
		return Output{}, ctx.Err()
	}
}

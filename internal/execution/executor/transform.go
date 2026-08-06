package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"flowforge/internal/engine"
)

// transformConfig is the TRANSFORM node's config shape.
type transformConfig struct {
	Expression string `json:"expression"`
}

// Transform evaluates an expr expression returning any value, recorded under
// the "value" key.
type Transform struct{}

func (Transform) Type() string { return "TRANSFORM" }

func (Transform) Execute(ctx context.Context, in Input) (Output, error) {
	var cfg transformConfig
	if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
		return Output{}, fmt.Errorf("transform: invalid config: %w", err)
	}
	if cfg.Expression == "" {
		return Output{}, fmt.Errorf("transform: expression is empty")
	}

	val, err := engine.EvaluateTransform(ctx, cfg.Expression, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("transform: %w", err)
	}
	return Output{Data: map[string]any{"value": val}}, nil
}

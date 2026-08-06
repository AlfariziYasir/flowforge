package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"flowforge/internal/engine"
)

// conditionConfig is the CONDITION node's config shape.
type conditionConfig struct {
	Expression string `json:"expression"`
}

// Condition evaluates an expr boolean expression and records it under the
// "result" key — the exact key engine.CalculateReadyNodes reads in branchTaken.
type Condition struct{}

func (Condition) Type() string { return "CONDITION" }

func (Condition) Execute(ctx context.Context, in Input) (Output, error) {
	var cfg conditionConfig
	if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
		return Output{}, fmt.Errorf("condition: invalid config: %w", err)
	}
	if cfg.Expression == "" {
		return Output{}, fmt.Errorf("condition: expression is empty")
	}

	res, err := engine.EvaluateCondition(ctx, cfg.Expression, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("condition: %w", err)
	}
	return Output{Data: map[string]any{"result": res}}, nil
}

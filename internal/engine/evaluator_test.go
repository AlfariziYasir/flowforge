package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/engine"
)

// P-8: EvaluateCondition and EvaluateTransform boolean logic, comparisons, and steps.* references.
func TestEvaluator_BasicExpressions(t *testing.T) {
	ctx := context.Background()
	s := engine.Scope{
		Trigger: map[string]any{"amount": 150, "user": "alice"},
		Steps: map[string]engine.StepOutput{
			"check": {
				Status: engine.StepStatusSucceeded,
				Output: map[string]any{"approved": true, "code": 200},
			},
		},
	}

	t.Run("boolean condition logic", func(t *testing.T) {
		res, err := engine.EvaluateCondition(ctx, "trigger.amount > 100 && steps.check.output.approved", s)
		require.NoError(t, err)
		assert.True(t, res)

		resFalse, err := engine.EvaluateCondition(ctx, "trigger.amount < 50", s)
		require.NoError(t, err)
		assert.False(t, resFalse)
	})

	t.Run("transform value evaluation", func(t *testing.T) {
		out, err := engine.EvaluateTransform(ctx, "trigger.amount * 2", s)
		require.NoError(t, err)
		assert.Equal(t, 300, out)
	})
}

// P-8b: EvaluateCondition with lowercase trigger.name == 'x' compiles and runs against Scope.
func TestEvaluator_LowercaseScopeBinding(t *testing.T) {
	ctx := context.Background()
	s := engine.Scope{
		Trigger: map[string]any{"name": "x"},
	}

	res, err := engine.EvaluateCondition(ctx, "trigger.name == 'x'", s)
	require.NoError(t, err)
	assert.True(t, res)
}

// P-9: Non-boolean condition result errors.
func TestEvaluator_NonBooleanConditionError(t *testing.T) {
	ctx := context.Background()
	s := engine.Scope{Trigger: map[string]any{"msg": "hello"}}

	_, err := engine.EvaluateCondition(ctx, "trigger.msg", s)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrExpressionFailed)
}

// P-10: Syntax error is reported at compile time.
func TestEvaluator_SyntaxError(t *testing.T) {
	ctx := context.Background()
	s := engine.Scope{}

	err := engine.ValidateExpression("trigger.amount >", s)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrExpressionFailed)

	_, err = engine.EvaluateCondition(ctx, "trigger.amount >", s)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrExpressionFailed)
}

// P-11: Expression exceeding MaxExpressionNodes is rejected at compile time.
func TestEvaluator_MaxExpressionNodes(t *testing.T) {
	s := engine.Scope{Trigger: map[string]any{"x": 1}}

	// Generate expression with more than 1000 nodes: "x + 1 + 1 + 1 + ..."
	parts := make([]string, 1200)
	parts[0] = "trigger.x"
	for i := 1; i < 1200; i++ {
		parts[i] = "1"
	}
	hugeExpr := strings.Join(parts, " + ")

	err := engine.ValidateExpression(hugeExpr, s)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrExpressionFailed)
	assert.Contains(t, err.Error(), "exceeds maximum allowed nodes")
}

// P-12: An already-cancelled ctx returns immediately without evaluating.
func TestEvaluator_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := engine.Scope{Trigger: map[string]any{"x": 1}}

	_, err := engine.EvaluateCondition(ctx, "trigger.x == 1", s)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// P-13: Error message contains no Scope values (secret non-leakage).
func TestEvaluator_SecretNonLeakage(t *testing.T) {
	ctx := context.Background()
	secret := "SUPER_SECRET_TOKEN_999"
	s := engine.Scope{
		Variables: map[string]any{"secret": secret},
	}

	_, err := engine.EvaluateCondition(ctx, "variables.secret + 100", s)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "error message must not leak scope variable contents")
}

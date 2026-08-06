package engine_test

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
)

// P-14: Every StepStatus* constant is a member of the step_runs CHECK set as amended by migration 000002.
func TestStepStatus_CheckSetMatch(t *testing.T) {
	expectedSet := map[string]bool{
		"pending":   true,
		"ready":     true,
		"running":   true,
		"waiting":   true,
		"succeeded": true,
		"failed":    true,
		"retrying":  true,
		"skipped":   true,
	}

	actualConstants := []string{
		engine.StepStatusPending,
		engine.StepStatusReady,
		engine.StepStatusRunning,
		engine.StepStatusWaiting,
		engine.StepStatusSucceeded,
		engine.StepStatusFailed,
		engine.StepStatusRetrying,
		engine.StepStatusSkipped,
	}

	for _, status := range actualConstants {
		assert.True(t, expectedSet[status], "status %q must be in step_runs CHECK constraint set", status)
	}

	assert.Len(t, actualConstants, len(expectedSet))
}

// P-15: CanTransition accepts legal state machine edges and rejects invalid ones.
func TestStepStatus_CanTransition(t *testing.T) {
	valid := [][2]string{
		{engine.StepStatusPending, engine.StepStatusReady},
		{engine.StepStatusPending, engine.StepStatusSkipped},
		{engine.StepStatusReady, engine.StepStatusRunning},
		{engine.StepStatusReady, engine.StepStatusSkipped},
		{engine.StepStatusRunning, engine.StepStatusSucceeded},
		{engine.StepStatusRunning, engine.StepStatusFailed},
		{engine.StepStatusRunning, engine.StepStatusRetrying},
		{engine.StepStatusRunning, engine.StepStatusWaiting},
		{engine.StepStatusWaiting, engine.StepStatusSucceeded},
		{engine.StepStatusWaiting, engine.StepStatusFailed},
		{engine.StepStatusWaiting, engine.StepStatusSkipped},
		{engine.StepStatusRetrying, engine.StepStatusRunning},
		{engine.StepStatusRetrying, engine.StepStatusFailed},
		{engine.StepStatusRetrying, engine.StepStatusSkipped},
	}

	for _, pair := range valid {
		assert.True(t, engine.CanTransition(pair[0], pair[1]), "transition from %s to %s must be valid", pair[0], pair[1])
	}

	invalid := [][2]string{
		{engine.StepStatusSucceeded, engine.StepStatusRunning},
		{engine.StepStatusFailed, engine.StepStatusRunning},
		{engine.StepStatusWaiting, engine.StepStatusRunning},
		{engine.StepStatusSkipped, engine.StepStatusRunning},
	}

	for _, pair := range invalid {
		assert.False(t, engine.CanTransition(pair[0], pair[1]), "transition from %s to %s must be invalid", pair[0], pair[1])
	}

	assert.True(t, engine.IsTerminal(engine.StepStatusSucceeded))
	assert.True(t, engine.IsTerminal(engine.StepStatusFailed))
	assert.True(t, engine.IsTerminal(engine.StepStatusSkipped))
	assert.False(t, engine.IsTerminal(engine.StepStatusWaiting))
	assert.False(t, engine.IsTerminal(engine.StepStatusRunning))
}

// P-16: NextBackoff is exponential, capped at MaxDelay, and reproducible given fixed seed.
func TestRetryPolicy_NextBackoff(t *testing.T) {
	policy := engine.RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
	}

	rnd1 := rand.New(rand.NewSource(42))
	rnd2 := rand.New(rand.NewSource(42))

	b1 := policy.NextBackoff(1, rnd1)
	b2 := policy.NextBackoff(1, rnd2)
	assert.Equal(t, b1, b2, "jitter backoff must be reproducible given fixed seed")

	// Verify upper bound
	rnd3 := rand.New(rand.NewSource(100))
	for attempt := 1; attempt <= 10; attempt++ {
		backoff := policy.NextBackoff(attempt, rnd3)
		assert.LessOrEqual(t, backoff, 10*time.Second, "backoff must not exceed MaxDelay")
	}
}

// P-16b: EffectiveTimeout honours node config timeout, falls back to default, and caps at MaxStepTimeout.
func TestTimeoutPolicy_EffectiveTimeout(t *testing.T) {
	policy := engine.DefaultTimeoutPolicy()

	t.Run("falls back to default timeout when config has no timeoutSeconds", func(t *testing.T) {
		node := domain.NodeInput{Config: json.RawMessage(`{}`)}
		to, err := policy.EffectiveTimeout(node)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, to)
	})

	t.Run("honours custom positive timeoutSeconds from config", func(t *testing.T) {
		node := domain.NodeInput{Config: json.RawMessage(`{"timeoutSeconds": 60}`)}
		to, err := policy.EffectiveTimeout(node)
		require.NoError(t, err)
		assert.Equal(t, 60*time.Second, to)
	})

	t.Run("caps timeout at MaxStepTimeout", func(t *testing.T) {
		node := domain.NodeInput{Config: json.RawMessage(`{"timeoutSeconds": 3600}`)} // 1 hour
		to, err := policy.EffectiveTimeout(node)
		require.NoError(t, err)
		assert.Equal(t, engine.MaxStepTimeout, to) // 15 minutes
	})
}

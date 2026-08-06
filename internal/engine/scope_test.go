package engine_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/engine"
)

// P-5: Interpolate resolves {{trigger.x}}, {{steps.s1.output.y}}, {{variables.x}}, {{env.URL}}.
func TestInterpolate_ResolvesValidPaths(t *testing.T) {
	s := engine.Scope{
		Trigger: map[string]any{"name": "Alice"},
		Steps: map[string]engine.StepOutput{
			"http1": {
				Status: engine.StepStatusSucceeded,
				Output: map[string]any{"status_code": 200, "id": "u123"},
			},
		},
		Variables: map[string]any{"count": 42},
		Env:       map[string]string{"API_URL": "https://api.example.com"},
	}

	tmpl := "Hello {{trigger.name}}, status is {{steps.http1.output.status_code}}, count {{variables.count}} at {{env.API_URL}}"
	got, err := engine.Interpolate(tmpl, s)
	require.NoError(t, err)
	assert.Equal(t, "Hello Alice, status is 200, count 42 at https://api.example.com", got)
}

// P-6: Unresolvable paths return an error, never a silent empty string.
func TestInterpolate_UnresolvablePathErrors(t *testing.T) {
	s := engine.Scope{
		Trigger: map[string]any{"name": "Alice"},
	}

	tmpl := "Hello {{trigger.nonexistent_field}}"
	_, err := engine.Interpolate(tmpl, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolvable path")
}

// P-7: No-placeholder template passes through unchanged; unbalanced {{ errors.
func TestInterpolate_EdgeCases(t *testing.T) {
	s := engine.Scope{}

	t.Run("no placeholders passes through unchanged", func(t *testing.T) {
		got, err := engine.Interpolate("Plain text without variables", s)
		require.NoError(t, err)
		assert.Equal(t, "Plain text without variables", got)
	})

	t.Run("unbalanced braces error", func(t *testing.T) {
		_, err := engine.Interpolate("Hello {{trigger.name", s)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unbalanced template braces")
	})
}

// P-7b: ExprEnv produces lowercase top-level keys and renders Steps as nested maps.
func TestScope_ExprEnv(t *testing.T) {
	s := engine.Scope{
		Trigger: map[string]any{"foo": "bar"},
		Steps: map[string]engine.StepOutput{
			"s1": {
				Status: engine.StepStatusSucceeded,
				Output: map[string]any{"res": 100},
			},
		},
	}

	env := s.ExprEnv()
	assert.Contains(t, env, "trigger")
	assert.Contains(t, env, "steps")
	assert.Contains(t, env, "variables")
	assert.Contains(t, env, "env")

	stepsMap, ok := env["steps"].(map[string]any)
	require.True(t, ok)
	s1Map, ok := stepsMap["s1"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "succeeded", s1Map["status"])

	outMap, ok := s1Map["output"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 100, outMap["res"])
}

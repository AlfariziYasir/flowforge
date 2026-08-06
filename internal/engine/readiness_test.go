package engine_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
)

// P-17: CalculateReadyNodes linear, diamond, and join topologies.
func TestReadiness_Topologies(t *testing.T) {
	t.Run("linear topology ready calculation", func(t *testing.T) {
		g := domain.Graph{
			Nodes: []domain.NodeInput{
				node("a"), node("b"), node("c"),
			},
			Edges: []domain.EdgeInput{
				{From: "a", To: "b", Branch: "default"},
				{From: "b", To: "c", Branch: "default"},
			},
		}

		states := map[string]string{}
		s := engine.Scope{}

		ready, skipped, err := engine.CalculateReadyNodes(g, states, s)
		require.NoError(t, err)
		assert.Equal(t, []string{"a"}, ready)
		assert.Empty(t, skipped)

		states["a"] = engine.StepStatusSucceeded
		ready, skipped, err = engine.CalculateReadyNodes(g, states, s)
		require.NoError(t, err)
		assert.Equal(t, []string{"b"}, ready)
		assert.Empty(t, skipped)
	})

	t.Run("diamond topology ready calculation", func(t *testing.T) {
		g := domain.Graph{
			Nodes: []domain.NodeInput{
				node("root"), node("b"), node("c"), node("join"),
			},
			Edges: []domain.EdgeInput{
				{From: "root", To: "b", Branch: "default"},
				{From: "root", To: "c", Branch: "default"},
				{From: "b", To: "join", Branch: "default"},
				{From: "c", To: "join", Branch: "default"},
			},
		}

		states := map[string]string{"root": engine.StepStatusSucceeded, "b": engine.StepStatusSucceeded}
		s := engine.Scope{}

		ready, skipped, err := engine.CalculateReadyNodes(g, states, s)
		require.NoError(t, err)
		assert.Equal(t, []string{"c"}, ready, "join requires both b and c to succeed; c is ready, join is not yet ready")
		assert.Empty(t, skipped)
	})
}

// P-18: CONDITION evaluating false skips false branch transitively; true branch runs.
func TestReadiness_ConditionBranching(t *testing.T) {
	g := domain.Graph{
		Nodes: []domain.NodeInput{
			typedNode("cond", domain.NodeTypeCondition),
			node("true_branch"),
			node("false_branch"),
		},
		Edges: []domain.EdgeInput{
			{From: "cond", To: "true_branch", Branch: "true"},
			{From: "cond", To: "false_branch", Branch: "false"},
		},
	}

	states := map[string]string{"cond": engine.StepStatusSucceeded}
	s := engine.Scope{
		Steps: map[string]engine.StepOutput{
			"cond": {
				Status: engine.StepStatusSucceeded,
				Output: map[string]any{"result": true},
			},
		},
	}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"true_branch"}, ready)
	assert.Equal(t, []string{"false_branch"}, skipped)
}

// P-19: A failed predecessor leaves successor pending.
func TestReadiness_FailedPredecessor(t *testing.T) {
	g := domain.Graph{
		Nodes: []domain.NodeInput{node("a"), node("b")},
		Edges: []domain.EdgeInput{{From: "a", To: "b", Branch: "default"}},
	}

	states := map[string]string{"a": engine.StepStatusFailed}
	s := engine.Scope{}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, s)
	require.NoError(t, err)
	assert.Empty(t, ready)
	assert.Empty(t, skipped, "successor of failed node remains pending")
}

// P-19b: A waiting predecessor leaves successor pending.
func TestReadiness_WaitingPredecessor(t *testing.T) {
	g := domain.Graph{
		Nodes: []domain.NodeInput{node("a"), node("b")},
		Edges: []domain.EdgeInput{{From: "a", To: "b", Branch: "default"}},
	}

	states := map[string]string{"a": engine.StepStatusWaiting}
	s := engine.Scope{}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, s)
	require.NoError(t, err)
	assert.Empty(t, ready)
	assert.Empty(t, skipped, "successor of waiting node remains pending")
}

// P-20: Parallel independent branches both returned in one call.
func TestReadiness_ParallelBranches(t *testing.T) {
	g := domain.Graph{
		Nodes: []domain.NodeInput{node("root"), node("p1"), node("p2"), node("p3")},
		Edges: []domain.EdgeInput{
			{From: "root", To: "p1", Branch: "default"},
			{From: "root", To: "p2", Branch: "default"},
			{From: "root", To: "p3", Branch: "default"},
		},
	}

	states := map[string]string{"root": engine.StepStatusSucceeded}
	s := engine.Scope{}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"p1", "p2", "p3"}, ready)
	assert.Empty(t, skipped)
}

// W-1: The if/else-then-join pattern must not deadlock at the join. The join is
// ready once the live branch finishes, even though the other branch was skipped.
func TestReadiness_ConditionalJoin(t *testing.T) {
	g := graph(
		[]domain.NodeInput{
			typedNode("a", domain.NodeTypeCondition),
			node("b"),
			node("c"),
			node("d"),
		},
		[]domain.EdgeInput{
			{From: "a", To: "b", Branch: "true"},
			{From: "a", To: "c", Branch: "false"},
			{From: "b", To: "d", Branch: "default"},
			{From: "c", To: "d", Branch: "default"},
		},
	)

	scope := engine.Scope{
		Steps: map[string]engine.StepOutput{
			"a": {Status: engine.StepStatusSucceeded, Output: map[string]any{"result": true}},
		},
	}
	states := map[string]string{"a": engine.StepStatusSucceeded}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, ready)
	assert.Equal(t, []string{"c"}, skipped)

	states["b"] = engine.StepStatusSucceeded
	states["c"] = engine.StepStatusSkipped

	ready, skipped, err = engine.CalculateReadyNodes(g, states, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"d"}, ready, "join node must become ready once the live branch finishes")
	assert.Empty(t, skipped)
}

// W-1 (mirror): the false branch joins back just like the true branch.
func TestReadiness_ConditionalJoin_FalseBranch(t *testing.T) {
	g := graph(
		[]domain.NodeInput{
			typedNode("a", domain.NodeTypeCondition),
			node("b"),
			node("c"),
			node("d"),
		},
		[]domain.EdgeInput{
			{From: "a", To: "b", Branch: "true"},
			{From: "a", To: "c", Branch: "false"},
			{From: "b", To: "d", Branch: "default"},
			{From: "c", To: "d", Branch: "default"},
		},
	)

	scope := engine.Scope{
		Steps: map[string]engine.StepOutput{
			"a": {Status: engine.StepStatusSucceeded, Output: map[string]any{"result": false}},
		},
	}
	states := map[string]string{"a": engine.StepStatusSucceeded}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, ready)
	assert.Equal(t, []string{"b"}, skipped)

	states["c"] = engine.StepStatusSucceeded
	states["b"] = engine.StepStatusSkipped

	ready, skipped, err = engine.CalculateReadyNodes(g, states, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"d"}, ready, "join node must become ready once the false branch finishes")
	assert.Empty(t, skipped)
}

// W-1 guard: a node whose every inbound edge is dead becomes skipped, never ready.
func TestReadiness_AllBranchesSkipped(t *testing.T) {
	g := graph(
		[]domain.NodeInput{
			typedNode("a", domain.NodeTypeCondition),
			typedNode("b", domain.NodeTypeCondition),
			node("d"),
			node("x"),
			node("y"),
		},
		[]domain.EdgeInput{
			{From: "a", To: "d", Branch: "false"},
			{From: "a", To: "x", Branch: "true"},
			{From: "b", To: "d", Branch: "false"},
			{From: "b", To: "y", Branch: "true"},
		},
	)

	scope := engine.Scope{
		Steps: map[string]engine.StepOutput{
			"a": {Status: engine.StepStatusSucceeded, Output: map[string]any{"result": true}},
			"b": {Status: engine.StepStatusSucceeded, Output: map[string]any{"result": true}},
		},
	}
	states := map[string]string{
		"a": engine.StepStatusSucceeded,
		"b": engine.StepStatusSucceeded,
	}

	ready, skipped, err := engine.CalculateReadyNodes(g, states, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"x", "y"}, ready)
	assert.Equal(t, []string{"d"}, skipped, "node with only dead inbound edges must be skipped, not ready")
}

// W-2: A CONDITION that succeeded MUST carry a boolean output["result"]. Its
// absence is an executor bug — it errors, never silently skipping both branches.
func TestReadiness_ConditionMissingResult(t *testing.T) {
	g := graph(
		[]domain.NodeInput{
			typedNode("a", domain.NodeTypeCondition),
			node("b"),
			node("c"),
		},
		[]domain.EdgeInput{
			{From: "a", To: "b", Branch: "true"},
			{From: "a", To: "c", Branch: "false"},
		},
	)

	states := map[string]string{"a": engine.StepStatusSucceeded}

	tests := []struct {
		name  string
		scope engine.Scope
	}{
		{"step output absent entirely", engine.Scope{}},
		{"output present but result key missing", engine.Scope{
			Steps: map[string]engine.StepOutput{
				"a": {Status: engine.StepStatusSucceeded, Output: map[string]any{"statusCode": 200}},
			},
		}},
		{"result not a bool", engine.Scope{
			Steps: map[string]engine.StepOutput{
				"a": {Status: engine.StepStatusSucceeded, Output: map[string]any{"result": "true"}},
			},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, skipped, err := engine.CalculateReadyNodes(g, states, tt.scope)
			require.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrConditionResultMissing)
			assert.Empty(t, ready)
			assert.Empty(t, skipped)
		})
	}
}

// P-21: Output deterministic across 100 runs on wide fan-out.
func TestReadiness_DeterminismWideFanOut(t *testing.T) {
	nodes := []domain.NodeInput{node("root")}
	edges := []domain.EdgeInput{}

	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("worker_%02d", i)
		nodes = append(nodes, node(key))
		edges = append(edges, domain.EdgeInput{From: "root", To: key, Branch: "default"})
	}

	g := domain.Graph{Nodes: nodes, Edges: edges}
	states := map[string]string{"root": engine.StepStatusSucceeded}
	s := engine.Scope{}

	firstReady, _, err := engine.CalculateReadyNodes(g, states, s)
	require.NoError(t, err)
	require.Len(t, firstReady, 50)

	for run := 0; run < 100; run++ {
		r, _, err := engine.CalculateReadyNodes(g, states, s)
		require.NoError(t, err)
		assert.Equal(t, firstReady, r, "readiness output must be 100%% deterministic across repeated calls")
	}
}

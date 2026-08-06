package engine_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
)

func node(key string) domain.NodeInput {
	return domain.NodeInput{NodeKey: key, NodeType: domain.NodeTypeHTTP, Config: json.RawMessage(`{}`)}
}

func typedNode(key, nodeType string) domain.NodeInput {
	return domain.NodeInput{NodeKey: key, NodeType: nodeType, Config: json.RawMessage(`{}`)}
}

func edge(from, to string) domain.EdgeInput {
	return domain.EdgeInput{From: from, To: to}
}

func graph(nodes []domain.NodeInput, edges []domain.EdgeInput) domain.Graph {
	return domain.Graph{Nodes: nodes, Edges: edges}
}

// T-1: linear chain validates and orders A -> B -> C.
func TestValidateDAG_LinearChain(t *testing.T) {
	g := graph(
		[]domain.NodeInput{node("a"), node("b"), node("c")},
		[]domain.EdgeInput{edge("a", "b"), edge("b", "c")},
	)

	require.NoError(t, engine.ValidateDAG(g))

	order, err := engine.TopologicalOrder(g)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, order)
}

// T-2: diamond validates; the fan-out root comes first and the join last.
func TestValidateDAG_Diamond(t *testing.T) {
	g := graph(
		[]domain.NodeInput{node("a"), node("b"), node("c"), node("d")},
		[]domain.EdgeInput{edge("a", "b"), edge("a", "c"), edge("b", "d"), edge("c", "d")},
	)

	require.NoError(t, engine.ValidateDAG(g))

	order, err := engine.TopologicalOrder(g)
	require.NoError(t, err)
	require.Len(t, order, 4)
	assert.Equal(t, "a", order[0])
	assert.Equal(t, "d", order[3])
	// Deterministic tie-break: b before c, since both become ready together.
	assert.Equal(t, []string{"a", "b", "c", "d"}, order)
}

// T-3: a cycle is reported as ErrCycleDetected, not ErrInvalidDAG.
func TestValidateDAG_Cycle(t *testing.T) {
	g := graph(
		[]domain.NodeInput{node("a"), node("b"), node("c")},
		[]domain.EdgeInput{edge("a", "b"), edge("b", "c"), edge("c", "a")},
	)

	err := engine.ValidateDAG(g)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCycleDetected)
	assert.NotErrorIs(t, err, domain.ErrInvalidDAG)

	_, err = engine.TopologicalOrder(g)
	assert.ErrorIs(t, err, domain.ErrCycleDetected)
}

// T-4: a self-loop is a cycle. The DB CHECK also blocks it; the engine must not
// depend on the database to catch it.
func TestValidateDAG_SelfLoop(t *testing.T) {
	g := graph([]domain.NodeInput{node("a")}, []domain.EdgeInput{edge("a", "a")})

	err := engine.ValidateDAG(g)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrCycleDetected)
	assert.Contains(t, err.Error(), "a")
}

// T-5: an edge naming a node that does not exist.
func TestValidateDAG_DanglingEdge(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    domain.EdgeInput
	}{
		{"unknown from", edge("ghost", "a")},
		{"unknown to", edge("a", "ghost")},
		{"empty endpoint", edge("a", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graph([]domain.NodeInput{node("a")}, []domain.EdgeInput{tc.e})

			err := engine.ValidateDAG(g)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidDAG)
		})
	}
}

// T-6 / T-6b: node keys must be unique and non-empty.
func TestValidateDAG_NodeKeys(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		g := graph([]domain.NodeInput{node("a"), node("a")}, nil)

		err := engine.ValidateDAG(g)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
		assert.Contains(t, err.Error(), "duplicate")
	})

	t.Run("empty key", func(t *testing.T) {
		g := graph([]domain.NodeInput{node("")}, nil)

		err := engine.ValidateDAG(g)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
	})

	t.Run("whitespace-only key", func(t *testing.T) {
		g := graph([]domain.NodeInput{node("   ")}, nil)

		err := engine.ValidateDAG(g)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
	})
}

// T-7: an empty graph is not publishable.
func TestValidateDAG_EmptyGraph(t *testing.T) {
	err := engine.ValidateDAG(domain.Graph{})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidDAG)
	assert.Contains(t, err.Error(), "no nodes")

	_, err = engine.TopologicalOrder(domain.Graph{})
	assert.ErrorIs(t, err, domain.ErrInvalidDAG)
}

// T-8: determinism. This is the test that catches map-iteration ordering — a wide
// fan-out means many nodes are ready simultaneously, so an unordered ready-set
// produces a different permutation on nearly every run.
func TestTopologicalOrder_Deterministic(t *testing.T) {
	nodes := []domain.NodeInput{node("root")}
	var edges []domain.EdgeInput
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("leaf-%02d", i)
		nodes = append(nodes, node(key))
		edges = append(edges, edge("root", key))
	}
	g := graph(nodes, edges)

	first, err := engine.TopologicalOrder(g)
	require.NoError(t, err)
	require.Len(t, first, 41)

	for i := 0; i < 100; i++ {
		got, err := engine.TopologicalOrder(g)
		require.NoError(t, err)
		require.Equal(t, first, got, "TopologicalOrder must be byte-identical across runs (iteration %d)", i)
	}

	assert.Equal(t, "root", first[0])
	assert.Equal(t, "leaf-00", first[1])
	assert.Equal(t, "leaf-39", first[40])
}

// T-9: disconnected components are valid and all nodes appear.
func TestValidateDAG_DisconnectedComponents(t *testing.T) {
	g := graph(
		[]domain.NodeInput{node("a1"), node("a2"), node("b1"), node("b2")},
		[]domain.EdgeInput{edge("a1", "a2"), edge("b1", "b2")},
	)

	require.NoError(t, engine.ValidateDAG(g))

	order, err := engine.TopologicalOrder(g)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a1", "a2", "b1", "b2"}, order)
	assert.Less(t, indexOf(order, "a1"), indexOf(order, "a2"))
	assert.Less(t, indexOf(order, "b1"), indexOf(order, "b2"))
}

// T-9b: node types are constrained to the four the schema allows.
func TestValidateDAG_NodeType(t *testing.T) {
	t.Run("all supported types validate", func(t *testing.T) {
		g := graph([]domain.NodeInput{
			typedNode("h", domain.NodeTypeHTTP),
			typedNode("d", domain.NodeTypeDelay),
			typedNode("c", domain.NodeTypeCondition),
			typedNode("t", domain.NodeTypeTransform),
		}, nil)

		assert.NoError(t, engine.ValidateDAG(g))
	})

	for _, bad := range []string{"", "http", "WEBHOOK", "SQL"} {
		t.Run("rejects "+fmt.Sprintf("%q", bad), func(t *testing.T) {
			g := graph([]domain.NodeInput{typedNode("a", bad)}, nil)

			err := engine.ValidateDAG(g)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidDAG)
		})
	}
}

// T-9c: duplicate edges would violate uq_workflow_edges_version_from_to at insert
// time. Reject them here so the failure is a 409, not a raw 23505 turned 500.
func TestValidateDAG_DuplicateEdge(t *testing.T) {
	g := graph(
		[]domain.NodeInput{node("a"), node("b")},
		[]domain.EdgeInput{edge("a", "b"), edge("a", "b")},
	)

	err := engine.ValidateDAG(g)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidDAG)
	assert.Contains(t, err.Error(), "duplicate edge")
}

// A single node with no edges is a valid workflow.
func TestValidateDAG_SingleNode(t *testing.T) {
	g := graph([]domain.NodeInput{node("only")}, nil)

	require.NoError(t, engine.ValidateDAG(g))

	order, err := engine.TopologicalOrder(g)
	require.NoError(t, err)
	assert.Equal(t, []string{"only"}, order)
}

func TestValidateDAG_BranchValidation(t *testing.T) {
	t.Run("P-3: rejects condition node with default outgoing edge", func(t *testing.T) {
		g := graph(
			[]domain.NodeInput{typedNode("cond", domain.NodeTypeCondition), typedNode("b", domain.NodeTypeHTTP)},
			[]domain.EdgeInput{{From: "cond", To: "b", Branch: "default"}},
		)
		err := engine.ValidateDAG(g)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
		assert.Contains(t, err.Error(), "must specify branch 'true' or 'false'")
	})

	t.Run("P-4: rejects non-condition node with true or false edge", func(t *testing.T) {
		g := graph(
			[]domain.NodeInput{typedNode("http", domain.NodeTypeHTTP), typedNode("b", domain.NodeTypeHTTP)},
			[]domain.EdgeInput{{From: "http", To: "b", Branch: "true"}},
		)
		err := engine.ValidateDAG(g)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
		assert.Contains(t, err.Error(), "cannot specify branch")
	})

	t.Run("P-4b: condition node with two edges on different branches is valid", func(t *testing.T) {
		g := graph(
			[]domain.NodeInput{typedNode("cond", domain.NodeTypeCondition), typedNode("b", domain.NodeTypeHTTP)},
			[]domain.EdgeInput{
				{From: "cond", To: "b", Branch: "true"},
				{From: "cond", To: "b", Branch: "false"},
			},
		)
		assert.NoError(t, engine.ValidateDAG(g))
	})
}

func indexOf(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}

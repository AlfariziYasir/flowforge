package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
)

func node(key, nodeType string) domain.NodeInput {
	return domain.NodeInput{
		NodeKey:  key,
		NodeType: nodeType,
		Config:   json.RawMessage(`{}`),
	}
}

func edge(from, to string) domain.EdgeInput {
	return domain.EdgeInput{From: from, To: to}
}

// T-27: ToPersisted assigns distinct UUIDs and resolves every edge endpoint.
func TestGraph_ToPersisted(t *testing.T) {
	tenantID := uuid.New()
	versionID := uuid.New()
	now := time.Now()

	t.Run("assigns distinct node IDs and resolves edges", func(t *testing.T) {
		g := domain.Graph{
			Nodes: []domain.NodeInput{
				node("a", domain.NodeTypeHTTP),
				node("b", domain.NodeTypeDelay),
				node("c", domain.NodeTypeTransform),
			},
			Edges: []domain.EdgeInput{edge("a", "b"), edge("b", "c")},
		}

		nodes, edges, err := g.ToPersisted(tenantID, versionID, now)
		require.NoError(t, err)
		require.Len(t, nodes, 3)
		require.Len(t, edges, 2)

		seen := map[uuid.UUID]string{}
		for _, n := range nodes {
			assert.NotEqual(t, uuid.Nil, n.ID)
			_, dup := seen[n.ID]
			assert.False(t, dup, "node IDs must be distinct")
			seen[n.ID] = n.NodeKey

			assert.Equal(t, tenantID, n.TenantID)
			assert.Equal(t, versionID, n.WorkflowVersionID)
			assert.Equal(t, now, n.CreatedAt)
		}

		// Every edge endpoint must point at a real node UUID, and the mapping
		// must match the original keys.
		for _, e := range edges {
			assert.Equal(t, tenantID, e.TenantID)
			assert.Equal(t, versionID, e.WorkflowVersionID)
			assert.NotEqual(t, uuid.Nil, e.ID)
			_, okFrom := seen[e.FromNodeID]
			_, okTo := seen[e.ToNodeID]
			assert.True(t, okFrom, "from endpoint must resolve to a node")
			assert.True(t, okTo, "to endpoint must resolve to a node")
		}
		assert.Equal(t, "a", seen[edges[0].FromNodeID])
		assert.Equal(t, "b", seen[edges[0].ToNodeID])
		assert.Equal(t, "b", seen[edges[1].FromNodeID])
		assert.Equal(t, "c", seen[edges[1].ToNodeID])
	})

	t.Run("preserves node fields", func(t *testing.T) {
		g := domain.Graph{
			Nodes: []domain.NodeInput{{
				NodeKey:   "fetch",
				NodeType:  domain.NodeTypeHTTP,
				Config:    json.RawMessage(`{"url":"https://example.test"}`),
				PositionX: 12,
				PositionY: 34,
			}},
		}

		nodes, _, err := g.ToPersisted(tenantID, versionID, now)
		require.NoError(t, err)
		require.Len(t, nodes, 1)
		assert.Equal(t, "fetch", nodes[0].NodeKey)
		assert.Equal(t, domain.NodeTypeHTTP, nodes[0].NodeType)
		assert.JSONEq(t, `{"url":"https://example.test"}`, string(nodes[0].Config))
		assert.Equal(t, 12, nodes[0].PositionX)
		assert.Equal(t, 34, nodes[0].PositionY)
	})

	t.Run("defaults a nil config to an empty object", func(t *testing.T) {
		g := domain.Graph{Nodes: []domain.NodeInput{{NodeKey: "a", NodeType: domain.NodeTypeDelay}}}

		nodes, _, err := g.ToPersisted(tenantID, versionID, now)
		require.NoError(t, err)
		// config is NOT NULL DEFAULT '{}' in the schema; never write a nil.
		assert.JSONEq(t, `{}`, string(nodes[0].Config))
	})

	t.Run("rejects an edge naming an unknown node", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    domain.EdgeInput
		}{
			{"unknown from", edge("ghost", "a")},
			{"unknown to", edge("a", "ghost")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				g := domain.Graph{
					Nodes: []domain.NodeInput{node("a", domain.NodeTypeHTTP)},
					Edges: []domain.EdgeInput{tc.e},
				}

				_, _, err := g.ToPersisted(tenantID, versionID, now)
				require.Error(t, err)
				assert.ErrorIs(t, err, domain.ErrInvalidDAG)
				assert.Contains(t, err.Error(), "ghost")
			})
		}
	})

	t.Run("rejects duplicate node keys", func(t *testing.T) {
		g := domain.Graph{Nodes: []domain.NodeInput{
			node("a", domain.NodeTypeHTTP),
			node("a", domain.NodeTypeDelay),
		}}

		_, _, err := g.ToPersisted(tenantID, versionID, now)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
	})

	t.Run("empty graph yields empty slices, never nil", func(t *testing.T) {
		nodes, edges, err := domain.Graph{}.ToPersisted(tenantID, versionID, now)
		require.NoError(t, err)
		assert.NotNil(t, nodes)
		assert.NotNil(t, edges)
		assert.Empty(t, nodes)
		assert.Empty(t, edges)
	})
}

// T-28: FromPersisted(ToPersisted(g)) round-trips to g in canonical order.
func TestGraph_RoundTrip(t *testing.T) {
	tenantID := uuid.New()
	versionID := uuid.New()
	now := time.Now()

	original := domain.Graph{
		Nodes: []domain.NodeInput{
			node("alpha", domain.NodeTypeHTTP),
			node("beta", domain.NodeTypeCondition),
			node("gamma", domain.NodeTypeTransform),
		},
		Edges: []domain.EdgeInput{edge("alpha", "beta"), edge("alpha", "gamma")},
	}

	nodes, edges, err := original.ToPersisted(tenantID, versionID, now)
	require.NoError(t, err)

	got := domain.FromPersisted(nodes, edges)

	assert.Equal(t, original.Nodes, got.Nodes)
	assert.Equal(t, original.Edges, got.Edges)
}

// T-29: FromPersisted output is canonical — shuffled input, identical output.
func TestFromPersisted_Canonical(t *testing.T) {
	tenantID := uuid.New()
	versionID := uuid.New()
	now := time.Now()

	// Deliberately supplied out of order.
	src := domain.Graph{
		Nodes: []domain.NodeInput{
			node("zulu", domain.NodeTypeHTTP),
			node("alpha", domain.NodeTypeHTTP),
			node("mike", domain.NodeTypeHTTP),
		},
		Edges: []domain.EdgeInput{
			edge("zulu", "alpha"),
			edge("alpha", "mike"),
			edge("alpha", "zulu"),
		},
	}

	nodes, edges, err := src.ToPersisted(tenantID, versionID, now)
	require.NoError(t, err)

	t.Run("nodes sorted by key, edges by (from,to)", func(t *testing.T) {
		got := domain.FromPersisted(nodes, edges)

		assert.Equal(t, []string{"alpha", "mike", "zulu"}, keysOf(got.Nodes))
		assert.Equal(t, []domain.EdgeInput{
			edge("alpha", "mike"),
			edge("alpha", "zulu"),
			edge("zulu", "alpha"),
		}, got.Edges)
	})

	t.Run("row order does not affect output", func(t *testing.T) {
		baseline := domain.FromPersisted(nodes, edges)

		shuffledNodes := []domain.WorkflowNode{nodes[2], nodes[0], nodes[1]}
		shuffledEdges := []domain.WorkflowEdge{edges[1], edges[2], edges[0]}

		assert.Equal(t, baseline, domain.FromPersisted(shuffledNodes, shuffledEdges))
	})

	t.Run("empty input yields empty slices, never nil", func(t *testing.T) {
		got := domain.FromPersisted(nil, nil)
		assert.NotNil(t, got.Nodes)
		assert.NotNil(t, got.Edges)
		assert.Empty(t, got.Nodes)
		assert.Empty(t, got.Edges)
	})

	t.Run("an unresolvable endpoint surfaces as an empty key, not a dropped edge", func(t *testing.T) {
		// Corruption must stay visible: a dangling endpoint becomes an empty key
		// so ValidateDAG rejects the graph, rather than silently vanishing.
		orphan := domain.WorkflowEdge{
			ID:         uuid.New(),
			FromNodeID: nodes[0].ID,
			ToNodeID:   uuid.New(),
		}

		got := domain.FromPersisted(nodes, []domain.WorkflowEdge{orphan})
		require.Len(t, got.Edges, 1)
		assert.Equal(t, "", got.Edges[0].To)
	})
}

func keysOf(nodes []domain.NodeInput) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.NodeKey)
	}
	return out
}

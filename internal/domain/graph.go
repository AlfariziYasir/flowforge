package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// A workflow graph has two representations, and only the repository layer bridges them.
//
//   - Wire / logical form (Graph, NodeInput, EdgeInput): nodes are addressed by
//     NodeKey. This is what clients send, what validation reasons about, and what
//     the engine executes on. Clients cannot use UUIDs because on a fresh draft the
//     nodes do not exist yet.
//   - Persisted form (WorkflowNode, WorkflowEdge): nodes carry UUIDs and edges
//     reference them, matching the composite foreign keys on workflow_edges.
//
// Rule: the engine and use case speak keys; only the repository knows UUIDs.

// NodeInput is a graph node in wire form.
type NodeInput struct {
	NodeKey   string          `json:"nodeKey"`
	NodeType  string          `json:"nodeType"`
	Config    json.RawMessage `json:"config"`
	PositionX int             `json:"positionX"`
	PositionY int             `json:"positionY"`
}

// EdgeInput is a directed graph edge in wire form. From and To are node keys.
type EdgeInput struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the wire form of a workflow definition.
type Graph struct {
	Nodes []NodeInput `json:"nodes"`
	Edges []EdgeInput `json:"edges"`
}

// emptyConfig is the value written when a node supplies no config. The column is
// NOT NULL DEFAULT '{}', so a nil would be rejected by the database.
var emptyConfig = json.RawMessage(`{}`)

// ToPersisted assigns a fresh UUID to every node and resolves each edge's keys to
// those UUIDs, producing rows ready for insertion under the given version.
//
// It reports ErrInvalidDAG when node keys collide or an edge names a key that has
// no node. Structural validation beyond that — node types, cycles — belongs to
// internal/engine; this function only performs the translation it needs to be safe.
func (g Graph) ToPersisted(tenantID, versionID uuid.UUID, now time.Time) ([]WorkflowNode, []WorkflowEdge, error) {
	nodes := make([]WorkflowNode, 0, len(g.Nodes))
	idByKey := make(map[string]uuid.UUID, len(g.Nodes))

	for _, in := range g.Nodes {
		if _, exists := idByKey[in.NodeKey]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate node key %q", ErrInvalidDAG, in.NodeKey)
		}

		id := uuid.New()
		idByKey[in.NodeKey] = id

		config := in.Config
		if len(config) == 0 {
			config = emptyConfig
		}

		nodes = append(nodes, WorkflowNode{
			ID:                id,
			TenantID:          tenantID,
			WorkflowVersionID: versionID,
			NodeKey:           in.NodeKey,
			NodeType:          in.NodeType,
			Config:            config,
			PositionX:         in.PositionX,
			PositionY:         in.PositionY,
			CreatedAt:         now,
		})
	}

	edges := make([]WorkflowEdge, 0, len(g.Edges))
	for _, in := range g.Edges {
		fromID, ok := idByKey[in.From]
		if !ok {
			return nil, nil, fmt.Errorf("%w: edge references unknown node %q", ErrInvalidDAG, in.From)
		}
		toID, ok := idByKey[in.To]
		if !ok {
			return nil, nil, fmt.Errorf("%w: edge references unknown node %q", ErrInvalidDAG, in.To)
		}

		edges = append(edges, WorkflowEdge{
			ID:                uuid.New(),
			TenantID:          tenantID,
			WorkflowVersionID: versionID,
			FromNodeID:        fromID,
			ToNodeID:          toID,
			CreatedAt:         now,
		})
	}

	return nodes, edges, nil
}

// FromPersisted rebuilds the wire form from stored rows, mapping node UUIDs back
// to keys.
//
// The output is canonical: nodes are sorted by NodeKey and edges by (From, To),
// regardless of the order rows arrive in. Publish checksums depend on this — an
// identical graph must always marshal to identical bytes.
//
// An edge endpoint with no matching node yields an empty key rather than a dropped
// edge, so corruption surfaces as a validation failure instead of vanishing.
func FromPersisted(nodes []WorkflowNode, edges []WorkflowEdge) Graph {
	keyByID := make(map[uuid.UUID]string, len(nodes))
	for _, n := range nodes {
		keyByID[n.ID] = n.NodeKey
	}

	out := Graph{
		Nodes: make([]NodeInput, 0, len(nodes)),
		Edges: make([]EdgeInput, 0, len(edges)),
	}

	for _, n := range nodes {
		config := n.Config
		if len(config) == 0 {
			config = emptyConfig
		}
		out.Nodes = append(out.Nodes, NodeInput{
			NodeKey:   n.NodeKey,
			NodeType:  n.NodeType,
			Config:    config,
			PositionX: n.PositionX,
			PositionY: n.PositionY,
		})
	}

	for _, e := range edges {
		out.Edges = append(out.Edges, EdgeInput{
			From: keyByID[e.FromNodeID],
			To:   keyByID[e.ToNodeID],
		})
	}

	sort.Slice(out.Nodes, func(i, j int) bool {
		return out.Nodes[i].NodeKey < out.Nodes[j].NodeKey
	})
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From != out.Edges[j].From {
			return out.Edges[i].From < out.Edges[j].From
		}
		return out.Edges[i].To < out.Edges[j].To
	})

	return out
}

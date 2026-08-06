// Package engine holds FlowForge's pure workflow logic: DAG validation, execution
// ordering, and (from Phase 4) the deterministic executor itself.
//
// PURITY CONTRACT: this package may import only the standard library and
// flowforge/internal/domain. No HTTP frameworks, no database drivers, no Redis, no
// logging sinks. Everything here must be exercisable with plain values and no I/O.
// purity_test.go enforces this mechanically.
package engine

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"

	"flowforge/internal/domain"
)

// ValidateDAG reports whether g is a structurally valid workflow graph:
// non-empty, node keys unique and non-blank, node types supported, no duplicate
// edges, every edge endpoint resolving to a node, and no cycles.
//
// Cycles (including self-loops) yield domain.ErrCycleDetected. Every other defect
// yields domain.ErrInvalidDAG. Callers distinguish them with errors.Is to pick
// between WORKFLOW_CYCLE_DETECTED and WORKFLOW_INVALID_DAG.
func ValidateDAG(g domain.Graph) error {
	_, err := TopologicalOrder(g)
	return err
}

// TopologicalOrder returns g's node keys in execution order using Kahn's algorithm.
//
// The result is deterministic: whenever several nodes are ready at once, the
// lexicographically smallest key is emitted first. Identical input therefore always
// produces an identical slice, which is what makes workflow runs reproducible.
func TopologicalOrder(g domain.Graph) ([]string, error) {
	if err := validateStructure(g); err != nil {
		return nil, err
	}

	indegree := make(map[string]int, len(g.Nodes))
	for _, n := range g.Nodes {
		indegree[n.NodeKey] = 0
	}

	successors := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		successors[e.From] = append(successors[e.From], e.To)
		indegree[e.To]++
	}
	// Successor lists are built in edge order; sort them so the ready-set is fed
	// deterministically regardless of how edges arrived.
	for key := range successors {
		sort.Strings(successors[key])
	}

	ready := &stringHeap{}
	for _, n := range g.Nodes {
		if indegree[n.NodeKey] == 0 {
			*ready = append(*ready, n.NodeKey)
		}
	}
	heap.Init(ready)

	order := make([]string, 0, len(g.Nodes))
	for ready.Len() > 0 {
		key := heap.Pop(ready).(string)
		order = append(order, key)

		for _, next := range successors[key] {
			indegree[next]--
			if indegree[next] == 0 {
				heap.Push(ready, next)
			}
		}
	}

	if len(order) != len(g.Nodes) {
		return nil, fmt.Errorf("%w: nodes %s form a cycle", domain.ErrCycleDetected, formatKeys(remaining(indegree)))
	}

	return order, nil
}

// validateStructure performs every check that does not require traversal.
// Self-loops are handled here because Kahn's algorithm would otherwise report them
// as an ordinary cycle without naming the node.
func validateStructure(g domain.Graph) error {
	if len(g.Nodes) == 0 {
		return fmt.Errorf("%w: graph has no nodes", domain.ErrInvalidDAG)
	}

	known := make(map[string]string, len(g.Nodes)) // key -> nodeType
	for _, n := range g.Nodes {
		if strings.TrimSpace(n.NodeKey) == "" {
			return fmt.Errorf("%w: node key must not be blank", domain.ErrInvalidDAG)
		}
		if _, dup := known[n.NodeKey]; dup {
			return fmt.Errorf("%w: duplicate node key %q", domain.ErrInvalidDAG, n.NodeKey)
		}
		if !domain.IsValidNodeType(n.NodeType) {
			return fmt.Errorf("%w: node %q has unknown type %q", domain.ErrInvalidDAG, n.NodeKey, n.NodeType)
		}
		known[n.NodeKey] = n.NodeType
	}

	seenEdges := make(map[domain.EdgeInput]struct{}, len(g.Edges))
	for _, e := range g.Edges {
		fromType, ok := known[e.From]
		if !ok {
			return fmt.Errorf("%w: edge references unknown node %q", domain.ErrInvalidDAG, e.From)
		}
		if _, ok := known[e.To]; !ok {
			return fmt.Errorf("%w: edge references unknown node %q", domain.ErrInvalidDAG, e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("%w: node %q has an edge to itself", domain.ErrCycleDetected, e.From)
		}

		branch := e.Branch
		if branch == "" {
			branch = "default"
		}

		if fromType == domain.NodeTypeCondition {
			if branch != "true" && branch != "false" {
				return fmt.Errorf("%w: condition node %q outgoing edge must specify branch 'true' or 'false', got %q", domain.ErrInvalidDAG, e.From, e.Branch)
			}
		} else {
			if branch != "default" {
				return fmt.Errorf("%w: non-condition node %q outgoing edge cannot specify branch %q", domain.ErrInvalidDAG, e.From, e.Branch)
			}
		}

		normEdge := domain.EdgeInput{From: e.From, To: e.To, Branch: branch}
		if _, dup := seenEdges[normEdge]; dup {
			return fmt.Errorf("%w: duplicate edge %q -> %q (branch %q)", domain.ErrInvalidDAG, e.From, e.To, branch)
		}
		seenEdges[normEdge] = struct{}{}
	}

	return nil
}

// remaining returns the keys that never reached indegree zero — the cycle members
// plus anything downstream of them.
func remaining(indegree map[string]int) []string {
	keys := make([]string, 0, len(indegree))
	for key, deg := range indegree {
		if deg > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func formatKeys(keys []string) string {
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		quoted = append(quoted, fmt.Sprintf("%q", k))
	}
	return strings.Join(quoted, ", ")
}

// stringHeap is a min-heap of node keys, giving Kahn's algorithm a deterministic
// ready-set without re-sorting a slice on every pop.
type stringHeap []string

func (h stringHeap) Len() int           { return len(h) }
func (h stringHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h stringHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *stringHeap) Push(x any)        { *h = append(*h, x.(string)) }
func (h *stringHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

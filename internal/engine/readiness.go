package engine

import (
	"errors"
	"fmt"
	"sort"

	"flowforge/internal/domain"
)

// ErrConditionResultMissing is returned when a CONDITION node succeeded without
// producing a boolean output["result"]. The absence is an executor bug, not a
// valid state — silently skipping both branches would hide the workflow shape.
var ErrConditionResultMissing = errors.New("condition node succeeded without a boolean result")

// CalculateReadyNodes computes which nodes in graph g are ready to run next,
// and which nodes must be marked skipped due to untaken condition branches.
//
// Rules:
//  1. A node is ready when at least one inbound path is live (predecessor succeeded
//     and its branch was taken) AND no inbound path is still in motion.
//     This is an OR-join over dead branches, an AND-join over readiness.
//  2. A CONDITION that succeeded MUST produce a boolean output["result"]; its
//     absence is reported as ErrConditionResultMissing, never a silent skip.
//     A malformed CONDITION aborts the whole calculation, so an unrelated healthy
//     branch in the same run yields nothing either. This is deliberate: the run's
//     state is untrustworthy once a condition result is missing, and Phase 5 treats
//     the error as a failed run rather than partial progress.
//  3. A node all of whose inbound edges are dead is marked skipped, transitively.
//  4. A node with a failed, waiting, or still-running predecessor stays pending
//     (neither ready nor skipped).
//  5. Output ready and skipped slices are sorted lexicographically by node key.
func CalculateReadyNodes(g domain.Graph, states map[string]string, s Scope) (ready, skipped []string, err error) {
	if err := ValidateDAG(g); err != nil {
		return nil, nil, err
	}

	nodeTypeMap := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		nodeTypeMap[n.NodeKey] = n.NodeType
	}

	// Build inbound edges per node
	inboundEdges := make(map[string][]domain.EdgeInput, len(g.Nodes))
	for _, n := range g.Nodes {
		inboundEdges[n.NodeKey] = nil
	}
	for _, e := range g.Edges {
		inboundEdges[e.To] = append(inboundEdges[e.To], e)
	}

	// Compute topological order so we evaluate skipped status in topological order
	topoOrder, err := TopologicalOrder(g)
	if err != nil {
		return nil, nil, err
	}

	effectiveStates := make(map[string]string, len(g.Nodes))
	for k, v := range states {
		effectiveStates[k] = v
	}

	readySet := make(map[string]struct{})
	skippedSet := make(map[string]struct{})

	for _, nodeKey := range topoOrder {
		currentState := effectiveStates[nodeKey]
		if currentState == "" {
			currentState = StepStatusPending
		}

		// Only evaluate pending nodes for ready or skipped
		if currentState != StepStatusPending {
			continue
		}

		edges := inboundEdges[nodeKey]
		if len(edges) == 0 {
			// Root node with no inbound edges is ready if pending
			readySet[nodeKey] = struct{}{}
			continue
		}

		// Classify every inbound edge into exactly one category:
		//   live     — predecessor succeeded and its branch was taken (path done)
		//   dead     — predecessor skipped, or succeeded but branch not taken
		//              (this path will never arrive)
		//   blocking — predecessor still in motion (pending/ready/running/
		//              waiting/failed/retrying) or stuck (this path is not over)
		var live, blocking int

		for _, edge := range edges {
			predKey := edge.From
			predState := effectiveStates[predKey]
			if predState == "" {
				predState = StepStatusPending
			}

			switch predState {
			case StepStatusSucceeded:
				taken, err := branchTaken(nodeTypeMap[predKey], edge.Branch, s, predKey)
				if err != nil {
					return nil, nil, err
				}
				if taken {
					live++
				}
			case StepStatusSkipped:
				// dead
			default:
				blocking++
			}
		}

		switch {
		case blocking > 0:
			// stay pending — some path has not finished
		case live == 0:
			// every path is dead → skipped (transitively)
			skippedSet[nodeKey] = struct{}{}
			effectiveStates[nodeKey] = StepStatusSkipped
		default:
			// at least one live path, nothing blocking → ready
			readySet[nodeKey] = struct{}{}
		}
	}

	ready = make([]string, 0, len(readySet))
	for k := range readySet {
		ready = append(ready, k)
	}
	sort.Strings(ready)

	skipped = make([]string, 0, len(skippedSet))
	for k := range skippedSet {
		skipped = append(skipped, k)
	}
	sort.Strings(skipped)

	return ready, skipped, nil
}

// branchTaken reports whether the edge leaving predKey was taken, given the
// predecessor's node type, the edge branch, and the recorded step output.
//
// A CONDITION in status succeeded MUST carry a boolean output["result"]. Its
// absence is an executor bug and surfaces as ErrConditionResultMissing rather
// than silently killing both branches.
func branchTaken(predType, branch string, s Scope, key string) (bool, error) {
	if predType != domain.NodeTypeCondition {
		return branch == "default" || branch == "", nil
	}

	stepOut, ok := s.Steps[key]
	if !ok || stepOut.Output == nil {
		return false, fmt.Errorf("%w: node %q", ErrConditionResultMissing, key)
	}
	res, ok := stepOut.Output["result"]
	if !ok {
		return false, fmt.Errorf("%w: node %q", ErrConditionResultMissing, key)
	}
	boolRes, ok := res.(bool)
	if !ok {
		return false, fmt.Errorf("%w: node %q", ErrConditionResultMissing, key)
	}
	return boolRes == (branch == "true"), nil
}

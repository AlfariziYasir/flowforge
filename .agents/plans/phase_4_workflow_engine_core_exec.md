# Implementation Plan — Phase 4: Workflow Engine Core (v3)

Implements deterministic execution engine, sandboxed expression evaluator (`github.com/expr-lang/expr`), node readiness calculator, variable context scope interpolation, and edge branch support.

---

## Proposed Changes & Execution Steps

- [ ] **Step 1: Dependency & Purity Guard (P-22, P-22b)**:
  - Run `go get github.com/expr-lang/expr@v1.17.8`
  - Update `internal/engine/purity_test.go`: Add `"github.com/expr-lang/expr"` to `allowedPrefixes` with written rationale; add `"os/exec"` and `"net"` to `forbiddenImports`. Verify P-22 and P-22b.

- [ ] **Step 2: Migration 000002 & Graph Branch Discriminator (P-1, P-2, P-2b)**:
  - Create [migrations/000002_workflow_edge_branch.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000002_workflow_edge_branch.up.sql) and [migrations/000002_workflow_edge_branch.down.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000002_workflow_edge_branch.down.sql).
  - Update [internal/domain/graph.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/graph.go): Add `Branch` (`"default"`, `"true"`, `"false"`) to `EdgeInput`. Update `ToPersisted` / `FromPersisted` to sort edges by `(From, To, Branch)`. Add `Graph.Normalize()`.
  - Update [internal/domain/workflow.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/workflow.go): Add `Branch` field with `db:"branch"` to `WorkflowEdge`.
  - Extend [internal/domain/graph_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/graph_test.go) with P-1, P-2 (checksum collision red-first), P-2b.

- [ ] **Step 3: Repository & Handler Call Sites (B-5)**:
  - Update `ReplaceGraph` (insert list) and `LoadGraph` (select list) in [internal/workflow/repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository.go).
  - Update `SaveDraftGraph` request DTO / handler decoding in [internal/workflow/dto.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/dto.go) and [internal/workflow/handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go) to call `Normalize()`.

- [ ] **Step 4: DAG Branch Validation (P-3, P-4, P-4b)**:
  - Update [internal/engine/dag.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/dag.go): Validate that `CONDITION` outgoing edges are `true`/`false`, non-`CONDITION` edges are `default`. Update duplicate-edge check.
  - Extend [internal/engine/dag_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/dag_test.go).

- [ ] **Step 5: Step Execution State Machine & Policies (P-14, P-15, P-16, P-16b)**:
  - Create [internal/engine/state.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/state.go): `StepStatus*` constants (`pending`, `ready`, `running`, `waiting`, `succeeded`, `failed`, `retrying`, `skipped`), `IsTerminal`, `CanTransition`, `RetryPolicy.NextBackoff`, `TimeoutPolicy.EffectiveTimeout`.
  - Create [internal/engine/state_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/state_test.go).

- [ ] **Step 6: Scope & Expression Variable Bag (P-5, P-6, P-7, P-7b)**:
  - Create [internal/engine/scope.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/scope.go): `Scope`, `ExprEnv()`, `Interpolate()`.
  - Create [internal/engine/scope_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/scope_test.go).

- [ ] **Step 7: Sandboxed Expression Evaluator (P-8, P-8b, P-9, P-10, P-11, P-12, P-13)**:
  - Create [internal/engine/evaluator.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/evaluator.go): `MaxExpressionNodes = 1000`, `ValidateExpression`, `EvaluateCondition`, `EvaluateTransform`.
  - Create [internal/engine/evaluator_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/evaluator_test.go).

- [ ] **Step 8: Node Readiness Calculator (P-17, P-18, P-19, P-19b, P-20, P-21)**:
  - Create [internal/engine/readiness.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go): `CalculateReadyNodes(g domain.Graph, states map[string]string, s Scope) (ready, skipped []string, err error)`.
  - Create [internal/engine/readiness_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness_test.go).

---

## Verification Plan

- `make ci` (`gofmt`, `go vet`, `go build`, `go test ./... -race -count=1`).

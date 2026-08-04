# Implementation Plan — Phase 4: Workflow Engine Core (**v2**)

**Source docs:** `backlog.md` §Phase 4 · `database_design.md` §7.5 · `AGENTS.md`
**Predecessor:** Phase 3 (`internal/engine/dag.go`, `internal/domain/graph.go`) — code-complete, integration testing deferred by decision to a later phase.

> **v2 changelog.** v1 was reviewed against the real codebase and had four blocking gaps. v2 closes them: the graph model cannot currently express conditional branches (§2 D-1, now a schema change), the purity test rejects `expr-lang` on its first import (D-2), the evaluator had no cancellation channel despite promising a 50 ms timeout (D-3), and v1's step-status constants **do not match the `step_runs` CHECK constraint** (D-4). Scope also widened to the three backlog items v1 omitted: retry, timeout, and parallel-branch rules.

---

## 1. Task Summary

**Goal.** Make a validated DAG *executable*: resolve a run's variable scope, evaluate `CONDITION` and `TRANSFORM` expressions safely in memory, decide which nodes are ready at each tick, and define the step state machine with retry and timeout policy. All pure — no I/O, no database, no HTTP. Phase 5 supplies the worker that drives it.

**What already exists — do not rebuild:**

| Already done (Phase 3) | Location |
|---|---|
| Kahn's algorithm, cycle detection, deterministic topological order | `internal/engine/dag.go` — `ValidateDAG`, `TopologicalOrder` |
| Wire graph model + canonical ordering | `internal/domain/graph.go` — `Graph`, `NodeInput`, `EdgeInput`, `ToPersisted`, `FromPersisted` |
| Node type constants and validation | `internal/domain/workflow.go` — `IsValidNodeType` |
| Purity enforcement | `internal/engine/purity_test.go` — `TestEngine_ImportPurity` |

`backlog.md` lists "DAG validation / cycle detection / topological ordering" under Phase 4; those landed early in Phase 3. Phase 4 adds everything above that line.

**Out of scope, with owner:** step executors and SSRF validation (Phase 5) · queue and worker runtime (Phase 5) · run persistence (Phase 6).

---

## 2. Resolved Decisions

| # | Decision | Rationale |
|---|---|---|
| **D-1** | **Edges carry a `branch` label**; new migration `000002_workflow_edge_branch.up.sql` | Blocking. See below. |
| **D-2** | `expr-lang/expr` added to the engine's purity allowlist, with the rationale recorded in `purity_test.go` | It is a pure in-memory evaluator — no I/O, no reflection into the host. Allowing it silently would weaken a guard three review rounds established; allowing it *with a stated reason* keeps the guard meaningful. |
| **D-3** | Every evaluator entry point takes `context.Context` as its first parameter; the variable bag is renamed `Scope` | The 50 ms timeout needs something to cancel against. `reviewer.md` §3 already requires ctx-first. Naming it `ExecutionContext` alongside `context.Context` in the same signature is a readability trap. |
| **D-4** | Step statuses mirror the `step_runs` CHECK constraint **exactly** | v1 invented `StepStatusCompleted` and omitted `ready`/`retrying`. The DB says `succeeded`. Phase 5 writes these values directly to `step_runs.status` — a mismatch is a runtime constraint violation no unit test would catch. |

### D-1 in detail — the blocking gap

`readiness.go` must decide which branch of a `CONDITION` node to follow. It cannot today:

```go
type EdgeInput struct {          // internal/domain/graph.go
    From string `json:"from"`
    To   string `json:"to"`
}
```

`workflow_edges` has `from_node_id`, `to_node_id`, and no discriminator. The API specs are silent on how branches are modelled, so this is a genuinely open design decision rather than something to infer.

> [!IMPORTANT]
> **Chosen: (a) label the edge.** Control flow belongs in the graph, where `ValidateDAG` can already reach it. The alternative — routing inside the CONDITION node's `config` JSONB — needs no migration, but then edges no longer describe control flow and a config naming a nonexistent node slips past validation entirely.
>
> **This is a Phase 3 schema change landing in Phase 4.** Say so now if you would rather take (b) and keep `migrations/` frozen.

Shape:

```sql
-- 000002_workflow_edge_branch.up.sql
ALTER TABLE workflow_edges
    ADD COLUMN branch VARCHAR(50) NOT NULL DEFAULT 'default'
    CHECK (branch IN ('default', 'true', 'false'));

-- the uniqueness rule must widen with it: a CONDITION node may legitimately
-- have two edges to the same target, one per branch.
ALTER TABLE workflow_edges DROP CONSTRAINT uq_workflow_edges_version_from_to;
ALTER TABLE workflow_edges ADD CONSTRAINT uq_workflow_edges_version_from_to_branch
    UNIQUE (workflow_version_id, from_node_id, to_node_id, branch);
```

> [!WARNING]
> **The publish checksum depends on this.** `domain.FromPersisted` currently sorts edges by `(From, To)`. With a label it must sort by `(From, To, Branch)` — otherwise two structurally different graphs can serialise identically and produce the same checksum, silently breaking the immutability guarantee Phase 3's `MarkPublished` rests on. Same for `EdgeInput`'s JSON shape and the `ValidateDAG` duplicate-edge check, which currently keys on the whole `EdgeInput` struct and will pick up the new field for free.

Non-`CONDITION` nodes emit `branch = "default"`. `ValidateDAG` gains one rule: a `CONDITION` node's outgoing edges must all be `true`/`false`, never `default`; every other node's must be `default`.

---

## 3. Architecture — `internal/engine/` (PURE)

### 3.1 `scope.go` — the run's variable bag

```go
// Scope is the data a running workflow can see. It is a value: the engine never
// mutates a caller's Scope, and every evaluation is a pure function of it.
type Scope struct {
    Trigger   map[string]any        `json:"trigger"`
    Steps     map[string]StepOutput `json:"steps"`
    Variables map[string]any        `json:"variables"`
    Env       map[string]string     `json:"env"`
}

type StepOutput struct {
    Status string         `json:"status"`
    Output map[string]any `json:"output"`
}

// Interpolate substitutes {{path.to.value}} references in a template.
// Unresolvable paths are an error, never a silent empty string — a typo in a
// node config must fail the step, not send an empty header.
func Interpolate(tmpl string, s Scope) (string, error)
```

`Env` holds only values the caller explicitly places there. The engine must never read `os.Environ()` — that is the whole point of the purity contract, and `purity_test.go` will not catch it (`os` is stdlib). State it in the doc comment.

### 3.2 `evaluator.go` — sandboxed expressions

```go
// EvaluateCondition compiles and runs a CONDITION node's expression, returning
// its boolean result. Non-boolean results are an error.
func EvaluateCondition(ctx context.Context, expression string, s Scope) (bool, error)

// EvaluateTransform runs a TRANSFORM node's expression and returns its value.
func EvaluateTransform(ctx context.Context, expression string, s Scope) (any, error)
```

- Compile with `expr.Env(s)` plus `expr.AsBool()` (condition) / no type assertion (transform), and `expr.Timeout(...)`.
- Enforce **50 ms** by deriving `context.WithTimeout` from the caller's ctx and passing it to `expr.Run`. A caller that has already cancelled must return promptly without evaluating.
- Reject at compile time, not run time, where possible — a syntax error in a published workflow should surface as `domain.ErrInvalidDAG` at publish, not as a failed run. (Wiring that into `PublishVersion` is Phase 5's call; expose `ValidateExpression(expression string) error` here so it can.)
- Errors wrap a new sentinel `ErrExpressionFailed`; the message must not include the evaluated `Scope`, which carries user data.

### 3.3 `state.go` — the step state machine (D-4)

```go
// These strings are written verbatim to step_runs.status. They MUST match the
// CHECK constraint in migrations/000001_init_schema.up.sql:
//   CHECK (status IN ('pending','ready','running','succeeded','failed','retrying','skipped'))
const (
    StepStatusPending   = "pending"
    StepStatusReady     = "ready"
    StepStatusRunning   = "running"
    StepStatusSucceeded = "succeeded"   // NOT "completed"
    StepStatusFailed    = "failed"
    StepStatusRetrying  = "retrying"
    StepStatusSkipped   = "skipped"
)

func IsTerminal(status string) bool          // succeeded | failed | skipped
func CanTransition(from, to string) bool     // the legal edges of the machine
```

Also here: `StepExecutionResult`, and the retry/timeout policy the backlog requires:

```go
type RetryPolicy struct {
    MaxAttempts int           // default 3
    BaseDelay   time.Duration // default 1s
    MaxDelay    time.Duration // default 30s
}

// NextBackoff returns the delay before attempt n (1-indexed), exponential with
// full jitter, capped at MaxDelay. Deterministic given a *rand.Rand so it can
// be tested — never the global source.
func (p RetryPolicy) NextBackoff(attempt int, rnd *rand.Rand) time.Duration
```

Jitter with an injected source keeps the engine testable and honours `dag.go`'s existing determinism discipline.

### 3.4 `readiness.go` — what runs next

```go
// CalculateReadyNodes returns the node keys whose predecessors are all satisfied,
// in deterministic order (ascending node key), together with the keys that must
// be marked skipped because a CONDITION excluded their branch.
func CalculateReadyNodes(g domain.Graph, states map[string]string) (ready, skipped []string, err error)
```

Rules:

1. A node is ready when every inbound edge's source is `succeeded` **and** that edge's branch was taken.
2. A `CONDITION` node that succeeded records its result; the untaken branch's edges are dead. A node all of whose inbound edges are dead is `skipped`, transitively.
3. A node with any inbound `failed` predecessor is neither ready nor skipped — it stays `pending` for the caller's failure policy.
4. Ordering is ascending node key, matching `TopologicalOrder`'s tie-break, so two identical runs schedule identically.
5. **Parallel branches** (backlog): every ready node is returned in one call. Concurrency limits are Phase 5's; the engine's job is to say what *may* run, not how many.

The condition result must reach this function. Cleanest: `Scope.Steps[key].Output["result"]` for `CONDITION` nodes, read via a small `conditionResult(s Scope, key string) (bool, bool)` helper — no new parameter, and it keeps the engine's signature a function of `(graph, state)`.

### 3.5 `purity_test.go` — D-2

```go
var allowedPrefixes = []string{
    "flowforge/internal/domain",
    // expr-lang is a pure in-memory expression evaluator: no I/O, no host
    // reflection, no network. It is allowed because CONDITION/TRANSFORM nodes
    // need sandboxed evaluation and hand-rolling one would be worse. Anything
    // else added here needs the same justification in writing.
    "github.com/expr-lang/expr",
}
```

Add `os/exec` and `net` to `forbiddenImports` while here — the engine has no business with either, and `net/http` alone does not cover them.

---

## 4. Boundary Checklist

- [ ] `internal/engine/` imports only stdlib + `flowforge/internal/domain` + `expr-lang/expr` (T-26 extended).
- [ ] No `os.Environ()`, no `time.Now()` in a decision path — time comes in as a parameter.
- [ ] No global `rand`; jitter takes an injected `*rand.Rand`.
- [ ] Every step status string matches the `step_runs` CHECK constraint.
- [ ] `context.Context` is the first parameter of both evaluator entry points.
- [ ] Evaluation errors never embed `Scope` contents.
- [ ] `FromPersisted` sorts edges by `(From, To, Branch)`.

---

## 5. TDD Specification

Assertion library **testify**; named `t.Run` subtests; everything under `-race`.

### 5.1 `internal/domain/graph_test.go` — extend for D-1

- **P-1** `ToPersisted` round-trips `Branch`; a `CONDITION` node's two edges to the same target survive.
- **P-2** `FromPersisted` orders by `(From, To, Branch)`. ★ **Feed two graphs differing only in branch labels and assert their `json.Marshal` output differs** — this is the checksum-collision guard, and it is the highest-value test in the phase.

### 5.2 `internal/engine/dag_test.go` — extend

- **P-3** A `CONDITION` node with a `default` outgoing edge → `domain.ErrInvalidDAG`.
- **P-4** A non-`CONDITION` node with a `true`/`false` edge → `domain.ErrInvalidDAG`.

### 5.3 `scope_test.go`

- **P-5** `{{trigger.name}}`, `{{steps.s1.output.id}}`, `{{variables.x}}`, `{{env.API_URL}}` all resolve.
- **P-6** An unresolvable path errors — it does **not** yield an empty string.
- **P-7** A template with no placeholders passes through unchanged; `{{` unbalanced errors.

### 5.4 `evaluator_test.go`

- **P-8** Boolean logic, comparisons, and `steps.*` references evaluate correctly.
- **P-9** A non-boolean condition result errors.
- **P-10** A syntax error is reported at compile, with no evaluation attempted.
- **P-11** ★ A pathological expression exceeds 50 ms and returns a timeout error — **and the test itself completes**, proving cancellation actually fires rather than the goroutine leaking.
- **P-12** An already-cancelled `ctx` returns immediately without evaluating.
- **P-13** The error message contains no `Scope` values (feed a secret in `Variables`, assert its absence).

### 5.5 `state_test.go`

- **P-14** ★ Every `StepStatus*` constant is a member of the `step_runs` CHECK set. Table-driven against the literal list from the migration — this is the D-4 guard.
- **P-15** `CanTransition` accepts the legal machine and rejects e.g. `succeeded → running`.
- **P-16** `NextBackoff` is exponential, capped at `MaxDelay`, and reproducible for a fixed seed.

### 5.6 `readiness_test.go`

- **P-17** Linear, diamond, and join topologies.
- **P-18** ★ A `CONDITION` evaluating false skips the false branch transitively, and the true branch runs.
- **P-19** A failed predecessor leaves its successor `pending`, not ready and not skipped.
- **P-20** Two independent branches are both returned in one call (parallel rule).
- **P-21** Output order is deterministic across 100 runs on a wide fan-out.

### 5.7 `purity_test.go`

- **P-22** Extended allowlist still rejects `net/http`, `pgx`, `redis`, `os/exec`, `net`, and `flowforge/internal/{workflow,auth,platform}`.

---

## 6. Execution Order

1. `go get github.com/expr-lang/expr` · extend `purity_test.go` (P-22) — do this **first**, so the allowlist change is a deliberate commit rather than a reaction to a red test.
2. Migration `000002` + `domain.EdgeInput.Branch` + `ToPersisted`/`FromPersisted` + graph tests (P-1, P-2). **P-2 must be red before the sort change.**
3. `dag.go` branch validation (P-3, P-4).
4. `state.go` (P-14 … P-16) — before anything depends on the status strings.
5. `scope.go` (P-5 … P-7).
6. `evaluator.go` (P-8 … P-13).
7. `readiness.go` (P-17 … P-21).
8. `make ci`, then append a Phase 4 entry to `.agents/memory/action_history.md`.

**Reporting rule.** Runtime figures come from `-race` only.

---

## 7. Definition of Done

- [ ] Conditional branches are representable, validated, and honoured by readiness.
- [ ] Expressions evaluate in a sandbox with an enforced 50 ms ceiling and honour caller cancellation.
- [ ] Step statuses are provably a subset of the `step_runs` CHECK constraint (P-14).
- [ ] Readiness is deterministic across repeated runs (P-21).
- [ ] Retry backoff is exponential, capped, and reproducible.
- [ ] `internal/engine` still imports nothing but stdlib, `domain`, and `expr-lang` (P-22).
- [ ] `make ci` green, 0 data races.

---

## 8. Carried Forward

1. **V-1** — `ContextWithTx`/`TxFromContext` widened from `pgx.Tx` to `DBTX` ([phase_3_close_out_review.md](phase_3_close_out_review.md)). ~20 minutes; worth doing before Phase 4 lands on top.
2. **Integration testing deferred by decision** to a later phase. Nothing in Phases 1–3 has executed against a real Postgres or Redis — `getTestPool` calls `t.Skipf`, so the integration suite passes green while doing nothing. Phase 4 is pure and genuinely does not need a database, so this phase changes nothing about that exposure; the debt simply keeps accruing. **Migration `000002` adds to it** — it will be the second unapplied migration.
3. **Nothing is committed.** Phase 4 adds a `go.mod` dependency and a migration on top of an uncommitted Phase 2 + Phase 3.
4. **PostgreSQL 15+ floor** still recorded only in migration comments.

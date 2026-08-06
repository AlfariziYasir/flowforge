# Implementation Plan — Phase 4: Workflow Engine Core (**v3**)

**Planner:** Claude (Architect) · **Executor:** Gemini Flash 3.6
**Source docs:** `backlog.md` §Phase 4 · `database_design.md` §7.5, §7.8 · `AGENTS.md`
**Predecessor:** Phase 3 committed as `388e139`
**Reference:** [phase_4_workflow_engine_core_reviewer_v2.md](phase_4_workflow_engine_core_reviewer_v2.md) — the reviewer agent's v2, preserved unmodified.

> **v3 changelog.** v2 is a strong plan and most of it survives intact. What changed: every external claim was executed rather than assumed, and **four of them failed**. The `expr-lang` sandbox design (v2 §3.2) does not compile as written — `expr.Timeout` does not exist and `expr.Run` takes no `context`. Test P-11 is unachievable. `expr.Env(Scope{})` binds Go field names, so `trigger.name` will not compile. And D-1's edge change misses two Phase 3 call sites plus a snapshot-decode hazard. v3 also challenges D-6 on evidence.

---

## 0. Verification Log

Every factual claim in v2 was checked against the repository or executed. Recording the outcomes so v3's corrections are auditable rather than assertions.

| v2 claim | Result |
|---|---|
| Phase 3 code-complete | ✅ committed as `388e139`; `internal/engine/dag.go`, `internal/domain/graph.go`, `internal/workflow/*` all present |
| `step_runs` CHECK lacks `waiting`, says `succeeded` not `completed` | ✅ **confirmed** — `migration:145` is `('pending','ready','running','succeeded','failed','retrying','skipped')`. D-4 is a real defect v2 caught correctly. |
| `workflow_runs.trigger_type` CHECK is `('manual','webhook','cron')` | ✅ confirmed, `migration:120` |
| `EdgeInput` has no branch discriminator | ✅ confirmed, `internal/domain/graph.go` |
| `FromPersisted` sorts by `(From, To)` | ✅ confirmed |
| purity test rejects `expr-lang` on first import | ✅ confirmed — allowlist is `flowforge/internal/domain` only |
| `getTestPool` skips | ✅ confirmed — `t.Skip` unless `FLOWFORGE_INTEGRATION=1` |
| `backlog.md` renumbered for Phase 7 Event-Driven Steps | ✅ confirmed — phases now run to 13 |
| `phase_3_close_out_review.md` exists | ✅ confirmed |
| **`expr.Timeout(...)` option** | ❌ **does not exist** — see B-1 |
| **`expr.Run` accepts a context** | ❌ **it does not** — `func Run(program *vm.Program, env any) (any, error)` — see B-1 |
| **`expr.WithContext` cancels evaluation** | ❌ it threads ctx into ctx-accepting *function calls*; nothing interrupts the VM — see B-2 |
| **A pathological expression can exceed 50 ms (P-11)** | ❌ expr has no loop construct and a built-in memory budget — see B-3 |
| **`expr.Env(Scope{})` resolves `trigger.name`** | ❌ binds Go field names; `trigger` is `unknown name` — see B-4 |

Probe environment: `github.com/expr-lang/expr v1.17.8` (latest), Go 1.26.5.

---

## 1. What v2 Got Right — carried into v3 unchanged

Stated explicitly so the executor does not re-litigate settled ground:

- **D-4 (step statuses)** is the single most valuable finding in v2. Inventing `StepStatusCompleted` would have produced a constraint violation at the first `step_runs` insert in Phase 5, which no unit test would catch. Kept, and hardened by P-14.
- **D-1's diagnosis** — the graph genuinely cannot express conditional branches, and putting routing in the CONDITION node's `config` JSONB would let a config naming a nonexistent node slip past `ValidateDAG`. Kept; the *implementation* gains the missing call sites (B-5).
- **The checksum-collision warning** on `FromPersisted` sorting is exactly right and is the highest-value test in the phase. Kept as P-2.
- **Scope/readiness/retry architecture**, ctx-first signatures, no global `rand`, no `os.Environ()`, deterministic ordering. Kept.
- **The observation that Phase 4's DAG work already landed in Phase 3.** Kept.

---

## 2. Blocking Corrections

### B-1 — The sandbox design does not compile

v2 §3.2: *"Compile with `expr.Env(s)` plus `expr.AsBool()` … and `expr.Timeout(...)`"* and *"deriving `context.WithTimeout` from the caller's ctx and passing it to `expr.Run`"*.

```
$ go doc github.com/expr-lang/expr Run
func Run(program *vm.Program, env any) (any, error)

$ go doc github.com/expr-lang/expr | grep -E "func (Timeout|WithContext|MaxNodes)"
func MaxNodes(n uint) Option
func WithContext(name string) Option
```

There is no `Timeout` option, and `Run` has no context parameter. Both halves of the stated mechanism are unavailable.

### B-2 — `WithContext` is not cancellation

```
$ go doc github.com/expr-lang/expr WithContext
WithContext passes context to all functions calls with a context.Context argument.
```

It threads a ctx into *host functions you inject that accept one*. It does not interrupt the VM between instructions. With no ctx-accepting functions in scope — which is our case, since the sandbox exposes no host functions — it does nothing at all.

### B-3 — P-11 is unachievable, and the threat model was wrong

v2's P-11 wants a pathological expression to exceed 50 ms. Executed:

```
nested       elapsed=33ms   err=memory budget exceeded (1:28)
while        compile error: unexpected token Bracket("{") (1:14)
recursion    compile error: unexpected token Operator("=") (1:13)
```

**The language has no loops and no recursion.** Runtime is bounded by AST size × data size, and a built-in memory budget stops large allocations before wall-clock becomes interesting. A test asserting "this takes >50 ms" would be asserting behaviour the library actively prevents.

The real exposure is not a slow expression; it is a **large** one. That is bounded at compile time, not run time — which changes the design (§3.2).

### B-4 — `Scope` as an `expr` env binds the wrong names

```
struct env  trigger.name == "ok"     -> unknown name trigger (1:1)
struct env  Trigger.name == "ok"     -> <nil>
map env     trigger.name == "ok"     -> true
```

`expr.Env(Scope{})` exposes Go field names, so every expression would have to be written `Trigger.name`, `Steps.s1.output.id` — contradicting v2 §5.3's `{{trigger.name}}` and `{{steps.s1.output.id}}`. `json` tags do not change this.

### B-5 — D-1 misses two Phase 3 call sites

Adding `Branch` to `EdgeInput`/`WorkflowEdge` requires changes v2 does not list. Both of these enumerate columns explicitly, so neither picks up a new field automatically:

- [repository.go:472-474](../../internal/workflow/repository.go#L472-L474) — `ReplaceGraph` inserts `("id","tenant_id","workflow_version_id","from_node_id","to_node_id","created_at")`.
- [repository.go:487+](../../internal/workflow/repository.go#L487) — `LoadGraph` selects an explicit column list.

Plus the `SaveDraftGraph` request DTO and handler decoding, and the `GetVersion` response shape.

### B-6 — Stored snapshots decode to an invalid branch

`workflow_versions.graph_snapshot` holds marshalled `Graph` JSON. Existing snapshots have no `branch` key, so they decode to `Branch == ""` — which the new validation rule (v2 §2, "every other node's must be `default`") rejects. A previously-published workflow would fail validation on read.

No production data exists today, so this is cheap to fix now and expensive to discover later. **Normalise on decode**: treat `""` as `"default"` in `FromPersisted` and in a `Graph.Normalize()` applied after unmarshalling a snapshot.

---

## 3. Revised Decisions

| # | Decision | Change from v2 |
|---|---|---|
| **D-1** | Edges carry a `branch` label; migration `000002` | **Kept**, scope corrected per B-5/B-6 |
| **D-2** | `expr-lang/expr` added to the purity allowlist with written justification | **Kept** |
| **D-3** | **Expression safety is enforced at compile time via `expr.MaxNodes`, not by a runtime timeout.** Entry points still take ctx-first and honour an already-cancelled caller, but make no promise of mid-evaluation interruption. | **Rewritten** — B-1/B-2/B-3 |
| **D-4** | Step statuses mirror the `step_runs` CHECK exactly | **Kept** — v2's best finding |
| **D-5** | Add `StepStatusWaiting` + its readiness rule now | **Kept** |
| **D-6** | **Migration `000002` widens only what Phase 4 uses: `workflow_edges.branch` and `step_runs.status`.** `node_type` and `trigger_type` widening moves to the phase that introduces those values. | **Narrowed** — see below |
| **D-7** | *(new)* Per-step timeout is **policy data**, defined here and enforced by Phase 5's worker | Closes a backlog item v2 left open |

### D-6, narrowed — why the "widen now" argument does not hold

v2's rationale: *"Widening a CHECK on an empty table is one line; doing it once production data exists means an `ALTER TABLE` that locks and revalidates."*

The premise is false here. **No migration has ever been applied to any database.** `getTestPool` skips unless `FLOWFORGE_INTEGRATION=1`, and nothing in Phases 1–3 has run against a real Postgres. There is no table to lock, and there will not be one before Phases 5–7 ship their own migrations.

Against that, pre-widening has a real cost: a CHECK that admits `EVENT_PUBLISH` while no code can produce or consume it stops being a guard during the phases in between. If a Phase 5 bug writes `EVENT_PUBLISH` into `node_type`, the database accepts it silently.

**Keep in `000002`:** `workflow_edges.branch` (Phase 4 writes it) and `step_runs.status` gaining `waiting` (D-5 puts it in the state machine). **Move out:** `node_type` and `trigger_type` widening → the phases that introduce those values, as documented in v2 §8's own table.

### D-7 — per-step timeout

`backlog.md` Phase 4 lists "Timeout logic"; v2 covered only the (now-removed) expression timeout. Define the policy here as pure data; Phase 5's worker enforces it with `context.WithTimeout` around the executor call:

```go
type TimeoutPolicy struct {
    StepTimeout time.Duration // default 30s, per step
}

// EffectiveTimeout resolves a node's timeout: its config "timeoutSeconds" when
// present and positive, otherwise the policy default. Bounded to MaxStepTimeout
// so a published workflow cannot pin a worker indefinitely.
func (p TimeoutPolicy) EffectiveTimeout(node domain.NodeInput) (time.Duration, error)

const MaxStepTimeout = 15 * time.Minute
```

Keeping this in the engine means the ceiling is testable without a worker, and Phase 5 inherits a decided policy rather than inventing one.

---

## 4. Architecture — `internal/engine/` (PURE)

### 4.1 `scope.go`

```go
// Scope is the data a running workflow can see. It is a value: the engine never
// mutates a caller's Scope, and every evaluation is a pure function of it.
//
// Env holds only what the caller explicitly places here. The engine must never
// read os.Environ() — purity_test.go cannot catch that, since os is stdlib.
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

// ExprEnv renders the Scope as the map expr-lang binds against.
//
// This exists because expr.Env(Scope{}) would expose Go field names — expressions
// would read "Trigger.name" instead of "trigger.name". Verified against
// expr v1.17.8. One conversion point, one place to test.
func (s Scope) ExprEnv() map[string]any

// Interpolate substitutes {{path.to.value}} references in a template.
// Unresolvable paths are an error, never a silent empty string — a typo in a node
// config must fail the step, not send an empty header.
func Interpolate(tmpl string, s Scope) (string, error)
```

`ExprEnv` must render `Steps` as nested maps (`steps.s1.output.id`), not as `StepOutput` structs, or the same field-name problem reappears one level down. P-8b pins this.

### 4.2 `evaluator.go` — bounded, not timed (D-3)

```go
// MaxExpressionNodes caps the compiled AST. This is the real DoS guard: expr has
// no loop or recursion construct (verified — `while` and recursive `let` do not
// parse), so evaluation cost is bounded by AST size times data size. A runtime
// timeout is not available: expr.Run takes no context, and expr.WithContext only
// threads a context into ctx-accepting host functions, of which the sandbox has none.
const MaxExpressionNodes = 1000

// ValidateExpression compiles without running. Publish-time entry point, so a
// syntax error surfaces as a rejected publish rather than a failed run.
func ValidateExpression(expression string, s Scope) error

// EvaluateCondition compiles and runs a CONDITION expression. Non-boolean results
// are an error.
//
// ctx is honoured before compilation and before execution; evaluation itself
// cannot be interrupted mid-flight. That is acceptable because expression cost is
// bounded at compile time — see MaxExpressionNodes.
func EvaluateCondition(ctx context.Context, expression string, s Scope) (bool, error)

func EvaluateTransform(ctx context.Context, expression string, s Scope) (any, error)
```

Compile options: `expr.Env(s.ExprEnv())`, `expr.MaxNodes(MaxExpressionNodes)`, plus `expr.AsBool()` for conditions.

Errors wrap a new sentinel `ErrExpressionFailed`. **The message must not embed `Scope` contents** — it carries tenant data, and Phase 6 surfaces step errors through the API. P-13 asserts this with a planted secret.

> The honest limitation, stated in the doc comment so nobody re-derives it: if a hard wall-clock ceiling is ever required, it needs a goroutine plus `select`, and the orphaned goroutine cannot be killed — a *response* timeout, not an *execution* timeout. Do not build that in Phase 4; compile-time bounding is the correct guard for a language with no loops.

### 4.3 `state.go` (D-4, D-5, D-7)

```go
// These strings are written verbatim to step_runs.status. They MUST match the
// CHECK constraint as amended by migration 000002:
//   CHECK (status IN ('pending','ready','running','waiting','succeeded','failed','retrying','skipped'))
const (
    StepStatusPending   = "pending"
    StepStatusReady     = "ready"
    StepStatusRunning   = "running"
    StepStatusWaiting   = "waiting"   // D-5: suspended awaiting an external event
    StepStatusSucceeded = "succeeded" // NOT "completed"
    StepStatusFailed    = "failed"
    StepStatusRetrying  = "retrying"
    StepStatusSkipped   = "skipped"
)

func IsTerminal(status string) bool      // succeeded | failed | skipped
func CanTransition(from, to string) bool

type RetryPolicy struct {
    MaxAttempts int           // default 3
    BaseDelay   time.Duration // default 1s
    MaxDelay    time.Duration // default 30s
}

// NextBackoff returns the delay before attempt n (1-indexed): exponential with
// full jitter, capped at MaxDelay. Takes an injected source — never the global
// one — so the sequence is reproducible under test.
func (p RetryPolicy) NextBackoff(attempt int, rnd *rand.Rand) time.Duration
```

`waiting` is not terminal; the only exits are `succeeded`, `failed`, `skipped`. Nothing in Phase 4 enters it.

Use `math/rand/v2` (Go 1.26) unless `*rand.Rand` from v1 is needed for API symmetry — pick one and state it in the doc comment. Either satisfies "no global source".

### 4.4 `readiness.go`

```go
// CalculateReadyNodes returns the node keys whose predecessors are all satisfied,
// in deterministic order (ascending node key), together with the keys that must be
// marked skipped because a CONDITION excluded their branch.
func CalculateReadyNodes(g domain.Graph, states map[string]string, s Scope) (ready, skipped []string, err error)
```

Rules:

1. Ready when every inbound edge's source is `succeeded` **and** that edge's branch was taken.
2. A succeeded `CONDITION` records its result; the untaken branch's edges are dead. A node whose inbound edges are all dead is `skipped`, transitively.
3. A node with a `failed` predecessor is neither ready nor skipped — it stays `pending` for the caller's failure policy.
4. **(D-5)** Same for a `waiting` predecessor: not ready, not skipped, stays `pending`. Same guard as rule 3.
5. Ordering is ascending node key, matching `TopologicalOrder`'s tie-break.
6. **Parallel branches:** every ready node is returned in one call. Concurrency limits are Phase 5's; the engine says what *may* run, not how many.

The condition result reaches this via `Scope.Steps[key].Output["result"]`, read through a `conditionResult(s Scope, key string) (bool, bool)` helper. **v3 makes `Scope` an explicit parameter** rather than v2's implicit assumption — v2's signature took only `(g, states)` while its §3.4 prose required scope access, which would not have compiled.

### 4.5 Graph changes (D-1, B-5, B-6)

```go
type EdgeInput struct {
    From   string `json:"from"`
    To     string `json:"to"`
    Branch string `json:"branch"` // "default" | "true" | "false"
}
```

Every touch point, none optional:

| File | Change |
|---|---|
| `internal/domain/graph.go` | `EdgeInput.Branch`; `ToPersisted` writes it; `FromPersisted` reads it **and sorts by `(From, To, Branch)`**; `Normalize()` maps `""` → `"default"` (B-6) |
| `internal/domain/workflow.go` | `WorkflowEdge.Branch` with `db:"branch"` |
| `internal/engine/dag.go` | `CONDITION` outgoing edges must be `true`/`false`; all others `default`. The duplicate-edge check keys on the whole `EdgeInput`, so it picks up `Branch` for free |
| `internal/workflow/repository.go` | `ReplaceGraph` insert column list **and** `LoadGraph` select list (B-5) |
| `internal/workflow/dto.go` + `handler.go` | `SaveDraftGraph` request decoding; call `Normalize()` after decode |
| `migrations/000002_*.up.sql` / `.down.sql` | below |

```sql
-- 000002_workflow_edge_branch.up.sql
ALTER TABLE workflow_edges
    ADD COLUMN branch VARCHAR(50) NOT NULL DEFAULT 'default';
ALTER TABLE workflow_edges
    ADD CONSTRAINT chk_workflow_edges_branch CHECK (branch IN ('default','true','false'));

-- A CONDITION node may legitimately have two edges to the same target, one per branch.
ALTER TABLE workflow_edges DROP CONSTRAINT IF EXISTS uq_workflow_edges_version_from_to;
ALTER TABLE workflow_edges ADD CONSTRAINT uq_workflow_edges_version_from_to_branch
    UNIQUE (workflow_version_id, from_node_id, to_node_id, branch);

-- D-5: step_runs gains 'waiting'. The original CHECK is inline and unnamed, so
-- PostgreSQL auto-named it; IF EXISTS keeps this safe if that assumption is wrong,
-- and the replacement carries an explicit name so no future migration has to guess.
ALTER TABLE step_runs DROP CONSTRAINT IF EXISTS step_runs_status_check;
ALTER TABLE step_runs ADD CONSTRAINT chk_step_runs_status
    CHECK (status IN ('pending','ready','running','waiting','succeeded','failed','retrying','skipped'));
```

`000001` ships a `.down.sql`, so `000002` must too (house convention). Narrowing a CHECK fails if any row already holds a new value; since nothing is deployed, the down migration is a clean reversal — **state that assumption in a comment** so a future reader knows it was considered, not overlooked.

**Naming note (M-2):** auto-generated constraint names are a PostgreSQL implementation detail, not a contract. `DROP CONSTRAINT IF EXISTS` plus an explicitly named replacement removes the guesswork permanently.

### 4.6 `purity_test.go` (D-2)

```go
var allowedPrefixes = []string{
    "flowforge/internal/domain",
    // expr-lang is a pure in-memory expression evaluator: no I/O, no host
    // reflection, no network. Allowed because CONDITION/TRANSFORM need sandboxed
    // evaluation and hand-rolling one would be worse. Anything else added here
    // needs the same justification in writing.
    "github.com/expr-lang/expr",
}
```

Add `os/exec` and `net` to `forbiddenImports` — `net/http` alone does not cover them.

---

## 5. Boundary Checklist

- [ ] `internal/engine/` imports only stdlib + `flowforge/internal/domain` + `expr-lang/expr`.
- [ ] No `os.Environ()`; no `time.Now()` in a decision path — time arrives as a parameter.
- [ ] No global `rand`.
- [ ] Every step status string is a member of the `step_runs` CHECK set.
- [ ] `context.Context` is first on both evaluator entry points, and an already-cancelled ctx short-circuits.
- [ ] Evaluation errors never embed `Scope` contents.
- [ ] `FromPersisted` sorts edges by `(From, To, Branch)`.
- [ ] Every expression compiles under `expr.MaxNodes(MaxExpressionNodes)`.

---

## 6. TDD Specification

testify · named `t.Run` subtests · everything under `-race`.

### 6.1 `internal/domain/graph_test.go` — extend

- **P-1** `ToPersisted` round-trips `Branch`; a CONDITION node's two edges to one target both survive.
- **P-2** ★ `FromPersisted` orders by `(From, To, Branch)`. **Feed two graphs differing only in branch labels and assert `json.Marshal` output differs.** The checksum-collision guard — highest-value test in the phase. Must be red before the sort change.
- **P-2b** *(B-6)* A snapshot decoded without a `branch` key normalises to `"default"` and passes `ValidateDAG`.

### 6.2 `internal/engine/dag_test.go` — extend

- **P-3** A CONDITION node with a `default` outgoing edge → `domain.ErrInvalidDAG`.
- **P-4** A non-CONDITION node with a `true`/`false` edge → `domain.ErrInvalidDAG`.
- **P-4b** Two edges same `(from,to)` but different branch are **valid** (the duplicate-edge rule must not over-reject).

### 6.3 `scope_test.go`

- **P-5** `{{trigger.name}}`, `{{steps.s1.output.id}}`, `{{variables.x}}`, `{{env.API_URL}}` all resolve.
- **P-6** An unresolvable path errors — it does **not** yield an empty string.
- **P-7** No-placeholder template passes through unchanged; unbalanced `{{` errors.
- **P-7b** *(B-4)* `ExprEnv()` produces lowercase top-level keys and renders `Steps` as nested maps.

### 6.4 `evaluator_test.go`

- **P-8** Boolean logic, comparisons, and `steps.*` references evaluate correctly.
- **P-8b** ★ *(B-4)* `EvaluateCondition(ctx, "trigger.name == 'x'", scope)` compiles and runs. **Written lowercase.** This is the regression guard for the struct-env trap; with `expr.Env(Scope{})` it fails with `unknown name trigger`.
- **P-9** A non-boolean condition result errors.
- **P-10** A syntax error is reported at compile, with no evaluation attempted.
- **P-11** ★ **(rewritten, B-3)** An expression exceeding `MaxExpressionNodes` is **rejected at compile**. Generate it programmatically (e.g. a long chain of `+ 1`) and assert `ValidateExpression` returns an error naming the node budget. *(v2's "exceeds 50 ms" version is unachievable: expr has no loops or recursion, and its memory budget fires first — see §0.)*
- **P-12** An already-cancelled ctx returns immediately without evaluating.
- **P-13** The error message contains no `Scope` values — plant a secret in `Variables`, assert its absence.

### 6.5 `state_test.go`

- **P-14** ★ Every `StepStatus*` constant is a member of the `step_runs` CHECK set **as amended by `000002`**. Table-driven against the literal list. The D-4 guard.
- **P-15** `CanTransition` accepts the legal machine, rejects `succeeded → running`. Includes D-5: `running → waiting` legal, `waiting → running` not, `waiting` not terminal.
- **P-16** `NextBackoff` is exponential, capped at `MaxDelay`, reproducible for a fixed seed.
- **P-16b** *(D-7)* `EffectiveTimeout` honours a node's `timeoutSeconds`, falls back to the default, and refuses to exceed `MaxStepTimeout`.

### 6.6 `readiness_test.go`

- **P-17** Linear, diamond, and join topologies.
- **P-18** ★ A CONDITION evaluating false skips the false branch transitively; the true branch runs.
- **P-19** A `failed` predecessor leaves its successor `pending` — not ready, not skipped.
- **P-19b** *(D-5)* A `waiting` predecessor does the same.
- **P-20** Two independent branches both returned in one call.
- **P-21** ★ Output deterministic across 100 runs on a wide fan-out — same discipline that caught the map-ranged Kahn in Phase 3.

### 6.7 `purity_test.go`

- **P-22** Extended allowlist still rejects `net/http`, `pgx`, `redis`, `os/exec`, `net`, `flowforge/internal/{workflow,auth,platform}`.
- **P-22b** ★ **Prove the guard bites.** After extending the allowlist, temporarily add a forbidden import and confirm P-22 fails, then revert. Phase 3 did this for T-26 and T-8; an allowlist that has just been widened is exactly when the proof matters most.

---

## 7. Execution Order

1. `go get github.com/expr-lang/expr@v1.17.8` · extend `purity_test.go` (P-22, P-22b). **First**, so widening the allowlist is a deliberate commit, not a reaction to a red test.
2. Migration `000002` (up **and** down) · `EdgeInput.Branch` + `WorkflowEdge.Branch` + `Normalize()` · `ToPersisted`/`FromPersisted` · graph tests (P-1, P-2, P-2b). **P-2 red before the sort change.**
3. **B-5 call sites** — `ReplaceGraph` insert list, `LoadGraph` select list, `SaveDraftGraph` DTO/handler. `make ci` green here before moving on; this is where a missed column shows up.
4. `dag.go` branch validation (P-3, P-4, P-4b).
5. `state.go` (P-14 … P-16b) — before anything depends on the status strings.
6. `scope.go` including `ExprEnv` (P-5 … P-7b).
7. `evaluator.go` (P-8 … P-13).
8. `readiness.go` (P-17 … P-21).
9. `make ci`, then append a Phase 4 entry to `.agents/memory/action_history.md`.

**Reporting rule.** Runtime figures come from `-race` only.

---

## 8. Definition of Done

- [ ] Conditional branches representable, validated, honoured by readiness, and **persisted** (B-5).
- [ ] A snapshot written before `000002` still validates (P-2b).
- [ ] Expressions bounded at compile time and honour caller cancellation before evaluating.
- [ ] Expressions are written in lowercase scope paths and compile (P-8b).
- [ ] Step statuses provably a subset of the `step_runs` CHECK (P-14).
- [ ] Readiness deterministic across repeated runs (P-21).
- [ ] Retry backoff exponential, capped, reproducible; step timeout bounded (P-16, P-16b).
- [ ] Purity guard extended **and proven to still bite** (P-22b).
- [ ] `make ci` green, 0 data races.

---

## 9. Related Design Work

[design_event_driven_steps.md](design_event_driven_steps.md) — wait tokens, inbox/outbox, orphan events. Placement is settled in `backlog.md` (verified: phases now run to 13):

| Case | Phase | What Phase 4 owes it |
|---|---|---|
| **C** — publish to a queue | Phase 5 | nothing; `EVENT_PUBLISH` ships with Phase 5's migration (D-6 narrowed) |
| **A** — event as a trigger | Phase 6 | nothing; `queue`/`grpc` ship with Phase 6's migration |
| **B** — node that waits | Phase 7 | `waiting` status + readiness rule (D-5) — code **and** CHECK, both in `000002` |

---

## 10. Carried Forward

1. **V-1** — `ContextWithTx`/`TxFromContext` widened from `pgx.Tx` to `DBTX` ([phase_3_close_out_review.md](phase_3_close_out_review.md)). ~20 minutes; worth landing before Phase 4 builds on top.
2. **Integration testing deferred by decision.** Nothing in Phases 1–3 has executed against a real Postgres or Redis — `getTestPool` skips unless `FLOWFORGE_INTEGRATION=1`, so the integration suite passes green while doing nothing. Phase 4 is pure and does not need a database, so this phase adds no new exposure — but `000002` becomes the **second unapplied migration**, and the branch-column change touches Phase 3 SQL that has never run. That combination is worth a real `make up` before Phase 5.
3. **PostgreSQL 15+ floor** recorded only in migration comments.

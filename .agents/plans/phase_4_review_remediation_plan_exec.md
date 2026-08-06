# Execution Record — Phase 4 Review Remediation

Closes the 4 findings in [phase_4_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_review_findings.md) — 1 high (W-1), 1 medium (W-2), 2 low (W-3, W-4). Plan: [phase_4_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_review_remediation_plan.md).

**Status: DONE — all gates green.**

---

## Phase AE — Fix Readiness Classification (W-1, W-2, W-3)

### Step 1 — RED tests (added to `internal/engine/readiness_test.go`)

| Test | Finding | RED status |
|---|---|---|
| `TestReadiness_ConditionalJoin` | W-1 | RED — step 2 returned `ready=[]` instead of `[d]` |
| `TestReadiness_ConditionalJoin_FalseBranch` | W-1 | RED — mirror case |
| `TestReadiness_AllBranchesSkipped` | W-1 guard | GREEN (guard test, correct pre-fix) |
| `TestReadiness_ConditionMissingResult` (2 subtests) | W-2 | RED — no error returned |

### Step 2 — the fix (`internal/engine/readiness.go`)

- **W-1**: replaced the two inconsistent flags (`allPredecessorsSucceeded` + `allInboundEdgesDead`) with an explicit per-edge classification into exactly one of **live / dead / blocking**, then a gap-free switch:

  ```go
  switch {
  case blocking > 0:
      // stay pending — some path has not finished
  case live == 0:
      // every path is dead → skipped (transitively)
  default:
      // at least one live path, nothing blocking → ready
  }
  ```

  A `skipped` predecessor now lands in **dead** (not blocking), so a node with one live path becomes ready immediately. `allPredecessorsSucceeded` removed entirely.

- **W-2**: new sentinel `ErrConditionResultMissing`; `branchTaken` returns `fmt.Errorf("%w: node %q", ErrConditionResultMissing, key)` when a `CONDITION` is succeeded but `output["result"]` is missing or not a `bool`. Old `conditionResult` removed.
- **W-3**: doc comment now states the precise semantics — *OR-join over dead branches, AND-join over readiness*.

## Phase AF — Read the Snapshot Back (W-4)

`internal/workflow/usecase.go` `GetVersion`:

```go
if ver.Status == domain.VersionStatusDraft {
    // draft: assemble from nodes/edges — its snapshot is still '{}'
    g = domain.FromPersisted(nodes, edges)   // via LoadGraph
} else {
    // published: the snapshot is the truth
    json.Unmarshal(ver.GraphSnapshot, &g)    // error wrapped with versionID
    g.Normalize()                            // B-6: older snapshots carry no "branch" key
}
```

Tests added to `internal/workflow/usecase_test.go`:
- `TestGetVersion_PublishedReadsSnapshot` — published version reads the snapshot; **no `LoadGraph` expectation registered**, so any call to it fails the mock.
- `TestGetVersion_PublishedSnapshotNormalizesBranches` — a snapshot edge with no `branch` key normalises to `default`.
- Existing `TestGetVersion_DraftAssemblesGraphFromLoadGraph` (T-31) kept green — drafts still use `LoadGraph`.

## Verification Results

| Gate | Result |
|---|---|
| RED before fix (2 ★ cases) | ✅ `ConditionalJoin` (both branches), `ConditionMissingResult` (both subtests) failed |
| GREEN after fix | ✅ `go test ./internal/engine/ ./internal/workflow/` |
| Mutation: `skipped` → blocking | ✅ `ConditionalJoin` + `_FalseBranch` fail |
| Mutation: missing result → treated as valid false | ✅ `ConditionMissingResult` fails |
| `TestReadiness_FailedPredecessor` / `WaitingPredecessor` | ✅ stay green |
| `grep allPredecessorsSucceeded internal/engine/readiness.go` | ✅ empty |
| Purity guard `TestEngine_ImportPurity` | ✅ PASS |
| `make ci` (fmt-check → vet → build → test -race -count=1) | ✅ exit 0 |

## Notes for Phase 5

- `CalculateReadyNodes` can now return an error for a structurally valid graph (W-2) — the worker must treat it as a failed run, not panic.
- A node in neither `ready` nor `skipped` is still waiting on another path — legitimate, not a deadlock. After AE the only stall is a `failed`/`waiting` predecessor.
- Phase 5 should add a liveness check: no `running`/`ready`/`waiting` step while `pending` steps remain → mark the run failed.

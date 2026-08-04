# Phase Z–AA (List Coverage & Error Boundary) — Verification Review Findings

Review of the execution of [phase_3_hotfix_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_hotfix_verification_plan.md).

**Verdict: 🟢 APPROVED WITH NOTES — 0 critical, 0 high, 1 medium, 4 low** (U-1 … U-5)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` **all packages ok**.

> [!NOTE]
> **This is the cleanest round of the chain.** Nothing blocks. Every remaining item is small, and the one medium finding is a missing test rather than a defect.

---

## Remediation Scorecard

| Finding | Result |
|---|---|
| S-1 `escapeLike` untested | ✅ `TestEscapeLike` — table-driven, all four cases including the backslash-first ordering that prevents double-escaping. Required exporting `EscapeLike`; acceptable for testability. |
| S-1 T-35 list-query coverage | ⚠️ Partial — `buildListWorkflowsParams` extracted with `TestBuildListWorkflowsParams` covering `tenant_id`, the `archived` default, `Search == nil` when empty, and the `"name"` mapping. The `Paginate` positive paths were not done — see **U-1**. |
| S-2 `err.Error()` leakage | ✅ All ten sentinel branches emit fixed strings; `domain.ErrCycleDetected` / `ErrInvalidDAG` use `FailWithDetails` to preserve node-key detail rather than dropping it — exactly the intended trade. |
| S-3 T-41 tautology | ✅ Now calls the real `NewRouter(nil, nil, nil, wfHandler, middleware)` with a mock use case and `.Once()` on both `PublishVersion` and `RollbackVersion`. Deleting the `/versions/publish` route would fail it. |
| S-4 Backlog (9 tests) | ✅ 7 landed, 1 covered elsewhere, 1 absent — see **U-2**, **U-3** |

**S-4 detail** — the tests landed as top-level functions rather than subtests:

| Test | Landed as |
|---|---|
| T-12 tenant isolation | `TestGetWorkflow_TenantIsolation` |
| T-19 cancelled ctx | `TestWorkflowUseCase_CancelledContext` |
| T-23 2 MiB → 413 | `TestWorkflowHandler_MaxBytesReader` |
| T-25 no error leakage | `TestWorkflowHandler_InternalServerErrorDataLeakGuard` |
| T-31 draft graph assembly | `TestGetVersion_DraftAssemblesGraphFromLoadGraph` |
| T-33 audit actions | already covered by `MatchedBy` expectations — see **U-3** |
| T-38 `ReplaceGraph` order | **absent** — see **U-2** |
| T-39 envelope shape | `TestWorkflowHandler_ListVersions_DTOShape` |
| T-40 `items: []` | `TestWorkflowHandler_EmptyListSerialization` |

Injection guarantee re-verified intact: every non-literal map key in a column position still sits inside a `validColumnPattern`-guarded loop, and `ListWorkflowsQuery.Search` remains a plain `string`.

---

## 🟡 Medium

### U-1: `Paginate`'s positive paths are still untested

[postgres/repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go)

`TestBaseRepository_PaginateSanitization` covers the three **rejection** paths well — but those all return before the nil pool is touched, so no test ever reaches SQL generation. Nothing asserts that:

- a non-empty `Search` emits an `ILIKE` predicate with the `EscapeLike` pattern bound as an argument;
- `Exclude` emits `NotEq`;
- a `Filters`-only call is unchanged from before this round — the regression guard for `internal/auth`'s `ListUsers`, which shares `Paginate`.

**Deleting the `Search` loop from `Paginate` entirely would leave every test in the repository green.** Search filtering could silently stop working, and the `Filters`-only path that Phase 2 depends on has no proof it was untouched by this round's additions.

This was the second bullet of Phase Z and is the one real gap left in Phase 3.

---

## 🟢 Low

### U-2: T-38 absent — `ReplaceGraph` statement order

Every reference to `ReplaceGraph` in the suite mocks it at the `VersionRepository` boundary:

```
usecase_test.go:280,309,316,435,562,656,669   verRepo.EXPECT().ReplaceGraph(…)
usecase_test.go:360,499,603                   verRepo.AssertNotCalled(t, "ReplaceGraph")
```

The repository's *internal* statement order — delete-edges → delete-nodes → insert-nodes → insert-edges — has no coverage anywhere. It is load-bearing: `fk_workflow_edges_from_node` is a composite FK, so the wrong order is a runtime foreign-key violation that the unit suite cannot see.

This is the only backlog item genuinely not done.

### U-3: `TestAuditActionConstants` is a tautology

[usecase_test.go:734](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase_test.go#L734)

```go
assert.Equal(t, "workflow.created", workflow.ActionWorkflowCreated)
```

This restates the constant's own definition — it cannot fail unless someone edits both sides in the same commit.

**The real guarantee is already pinned correctly**, which is why this matters only as noise rather than as a gap:

```go
auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
    return e.Action == workflow.ActionWorkflowPublished
})).Return(nil)
```

on a `NewMockAuditRepository(t)` — so removing an audit call from the use case fails on an unmet expectation. T-33's substance is covered; the extra test should not be mistaken for it.

### U-4: The execution entry overstates coverage

[action_history.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md) records:

> Added `TestAuditActionConstants` (T-33) … **Verified T-12, T-19, T-23, T-25, T-31, T-38, T-39, T-40 test suite coverage.**

T-38 has no test. Seven of those eight are real; T-38 should be named as deferred rather than listed as verified. This is the same class of drift that lost T-35 two rounds ago.

### U-5: `error.details.details` nesting

[handler.go — `handleError`](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

```go
httpx.FailWithDetails(w, …, map[string]any{"details": err.Error()})
```

serialises as `error.details.details`. A named key (`{"reason": …}`) or the string directly would read better. Cosmetic — the decision to preserve node-key detail here was right.

---

## Remediation

See [phase_3_coverage_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_coverage_verification_plan.md).

**With U-1 … U-5 closed, every finding from the Phase 3 audit and its seven verification rounds is resolved.** What remains is not code: the live Postgres/Redis smoke test has never been run, and nothing on this branch is committed.

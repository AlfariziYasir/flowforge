# Execution Record — Phase AG (X-1, X-2)

Closes the 2 items in [phase_4_remediation_verification.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_remediation_verification.md) — 1 medium (X-1), 1 low (X-2).

**Status: DONE — all gates green.**

---

## Phase AG — Close the Test Gap

### X-1 — cover guard 2 of `branchTaken` (`internal/engine/readiness_test.go`)

Renamed the misnamed subtest and added the missing third case:

| Subtest | Guard covered |
|---|---|
| `step output absent entirely` (renamed from `result key absent`) | 1 — step absent / `Output` nil |
| `output present but result key missing` (**new**) | 2 — `Output` present, `"result"` key missing |
| `result not a bool` | 3 — `"result"` present but not a `bool` |

**Mutation gate re-run**: replacing guard 2 alone with `return false, nil` now fails the test:

```
go test ./internal/engine/ -run TestReadiness_ConditionMissingResult
--- FAIL: TestReadiness_ConditionMissingResult
```

Guard 2 is the most realistic executor bug (a `CONDITION` executor that writes `{"statusCode": 200}` and forgets `result`), so it is now pinned.

### X-2 — record the blast radius (`internal/engine/readiness.go`)

Added to rule 2 of the `CalculateReadyNodes` doc comment: a malformed `CONDITION` aborts the whole calculation, so an unrelated healthy branch yields nothing either. Deliberate — the run's state is untrustworthy once a condition result is missing; Phase 5 treats the error as a failed run, not partial progress.

## Verification Results

| Gate | Result |
|---|---|
| All 3 subtests pass after the fix | ✅ |
| Mutation: guard 2 → `return false, nil` | ✅ `TestReadiness_ConditionMissingResult` FAILS |
| Restored after mutation | ✅ |
| `grep allPredecessorsSucceeded internal/engine/readiness.go` | ✅ empty |
| `make ci` (fmt-check → vet → build → test -race -count=1) | ✅ exit 0 |

## Deferred (per plan's own marking)

- `TestReadiness_NestedConditions` — the review's optional item. Behaviour verified by direct probe (nested conditions → `ready=[d] skipped=[c e]`); adding the test is cheap and worth doing before Phase 5, but was explicitly "worth considering rather than doing now". Left for a future pass.

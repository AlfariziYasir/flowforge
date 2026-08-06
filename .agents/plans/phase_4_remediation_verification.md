# Phase AE–AF (Phase 4 Remediation) — Verification Review & Remaining Work

Review of the execution of [phase_4_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_review_remediation_plan.md).

**Verdict: 🟡 CONDITIONAL — 0 critical, 0 high, 1 medium, 1 low** (X-1, X-2)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l` empty · `go test ./... -race -count=1` **all packages ok**.

> [!NOTE]
> **Findings and remediation are combined in one document.** One medium and one low item do not warrant a separate pair — consistent with `phase_2_final_polish.md`. All other `.agents/` conventions unchanged.

---

## Scorecard

| Finding | Result |
|---|---|
| **W-1** — conditional join deadlock | ✅ Fixed. Three-category classification (live/dead/blocking) implemented exactly as specified |
| **W-2** — silent skip on missing `result` | ✅ Fixed in code — all three paths return `ErrConditionResultMissing`. Test coverage incomplete, see **X-1** |
| **W-3** — doc comment vs implementation | ✅ Rewritten; now states the OR-join/AND-join semantics precisely |
| **W-4** — snapshot never read back | ✅ Fixed, and **better than specified** — see below |
| Gate: `allPredecessorsSucceeded` removed | ✅ `grep` returns empty — the flag was deleted, not patched around |

### Mutation verification — the gate this plan set

| Mutation | Expected | Result |
|---|---|---|
| Move `skipped` back into **blocking** | `TestReadiness_ConditionalJoin` fails | ✅ **Fails** — *"join node must become ready once the live branch finishes"* |
| `branchTaken` returns `(false, nil)` when `result` is missing | `TestReadiness_ConditionMissingResult` fails | ❌ **Still passes** — see **X-1** |

`TestReadiness_FailedPredecessor` and `TestReadiness_WaitingPredecessor` stayed green, as required.

### Behaviour probes

Beyond the plan, three scenarios were exercised directly against the new logic:

```
output present but no result key  → err=ErrConditionResultMissing: node "a"    ✅ correct
nested conditions (A→B→{c,d}, e)  → ready=[d] skipped=[c e]                    ✅ correct
```

Nested conditions resolve correctly, and the transitive skip propagates through a two-level branch — neither is covered by an existing test but both behave properly.

### W-4 implemented better than specified

`TestGetVersion_PublishedReadsSnapshot` registers **no `LoadGraph` expectation at all**, so mockery fails the test if the use case reaches for it. That is a stronger proof than the "make the two differ" approach the plan asked for: it makes the wrong behaviour impossible to pass rather than merely detectable.

---

## 🟡 Medium

### X-1: `TestReadiness_ConditionMissingResult` covers 2 of 3 paths and fails the mutation gate

[readiness_test.go:261-296](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness_test.go#L261-L296)

`branchTaken` has three routes to `ErrConditionResultMissing`:

| # | Guard | Condition | Covered? |
|---|---|---|---|
| 1 | [:150-153](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L150-L153) | step output absent, or `Output` nil | ✅ |
| 2 | [:154-157](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L154-L157) | `Output` present, `"result"` key missing | ❌ **not covered** |
| 3 | [:158-161](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L158-L161) | `"result"` present but not a `bool` | ✅ |

The subtest named `"result key absent"` passes `engine.Scope{}` — an entirely empty scope. `s.Steps` is nil, so lookup fails at guard **1**, never reaching guard 2. The name promises coverage the case does not deliver.

Proven by mutation — replacing guard 2 alone with `return false, nil`:

```
go test -run TestReadiness_ConditionMissingResult
ok  	flowforge/internal/engine	0.004s      ← should have failed
```

**The implementation is correct.** A direct probe confirms guard 2 fires:

```
output present but no result key → err=condition node succeeded without a boolean result: node "a"
```

So this is test debt, not a live defect. It matters because guard 2 is **the most realistic executor bug**: a `CONDITION` executor that writes `{"statusCode": 200}` and forgets `result`. Guards 1 and 3 describe an executor that wrote nothing at all, or wrote a string — both less likely than simply omitting a key.

---

## 🟢 Low

### X-2: One malformed `CONDITION` fails the entire run, including unrelated branches

The W-2 error aborts `CalculateReadyNodes` wholesale. A graph with two independent branches — one carrying a buggy condition, one entirely healthy — yields nothing:

```
graph:  a(CONDITION) --true--> b        (a succeeded, no result → broken)
        x --default--> y                (x succeeded → y should be ready)

result: ready=[] skipped=[] err=ErrConditionResultMissing: node "a"
```

Node `y` is blameless and ready by every rule, but never surfaces.

This is defensible — the plan's own Phase 5 note says the worker must "treat that as a failed run", and a malformed condition means the run's state is untrustworthy. But the **blast radius was never stated**: one branch's executor bug fails the whole run, including work that could have completed. Phase 5 should inherit that as a decision, not discover it.

---

## Remediation

### Phase AG — Close the Test Gap (X-1, X-2)

#### [MODIFY] [readiness_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness_test.go)

- **X-1** — add the missing third case to `TestReadiness_ConditionMissingResult`'s table, and rename the existing one so the names match what they exercise:

  ```go
  {"step output absent entirely", engine.Scope{}},                      // renamed from "result key absent"
  {"output present but result key missing", engine.Scope{               // NEW — guard 2
      Steps: map[string]engine.StepOutput{
          "a": {Status: engine.StepStatusSucceeded,
                Output: map[string]any{"statusCode": 200}},
      },
  }},
  {"result not a bool", /* unchanged */},
  ```

- **Re-run the mutation** afterwards. Replacing guard 2 with `return false, nil` must now fail the test. If it still passes, the new case is landing on the wrong guard.

#### [MODIFY] [readiness.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go)

- **X-2** — record the blast radius in the `CalculateReadyNodes` doc comment, next to rule 2:

  ```
  //     A malformed CONDITION aborts the whole calculation, so an unrelated healthy
  //     branch in the same run yields nothing either. This is deliberate: the run's
  //     state is untrustworthy once a condition result is missing, and Phase 5 treats
  //     the error as a failed run rather than partial progress.
  ```

*(Optional, worth considering rather than doing now: the probes showed nested conditions and two-level transitive skips both work but have no test. Adding `TestReadiness_NestedConditions` would pin behaviour the current suite only covers by accident.)*

### Verification

```
make ci                                    # must exit zero

# X-1 gate — mutate guard 2 only, keeping fmt in use:
#   readiness.go:154-157  →  return false, nil
go test ./internal/engine/ -run TestReadiness_ConditionMissingResult -count=1
# MUST fail. Restore afterwards.

grep -n "allPredecessorsSucceeded" internal/engine/readiness.go   # must stay empty
```

The purity guard (P-22b) was re-verified this round and still bites; no action needed.

---

## Notes for Phase 5

Carried forward from the previous plan, plus one addition from X-2:

- `CalculateReadyNodes` can return an **error** for a structurally valid graph. Treat it as a failed run, not a panic.
- **New (X-2):** that failure is run-wide. A single malformed `CONDITION` stops unrelated parallel branches too. If partial progress ever becomes desirable, it needs a deliberate redesign — scoping the error to the affected subgraph — not a local patch.
- A node in neither `ready` nor `skipped` is **still waiting on another path** — legitimate, not a deadlock. After W-1, the only remaining stall is a `failed`/`waiting` predecessor.
- Worth a liveness check in the worker: a run with no `running`/`ready`/`waiting` step while `pending` steps remain is stuck and should be marked failed.

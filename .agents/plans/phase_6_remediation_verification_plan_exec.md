# Execution Record — Phase AM (AA-1)

Plan: [phase_6_remediation_verification.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_6_remediation_verification.md). **Status: DONE — both suites green, mutation verified to fail.**

## Phase AM — Close the Field-Name Gap (AA-1)

Split the previously-shared step view into two, because the two endpoints' documented shapes genuinely differ:

- `newStepSummaryView` — `ListSteps` (api-3.md §10.2.1): `stepRunId`, `workflowNodeId`, `nodeKey`, `status`, `attemptCount`, `startedAt`, `finishedAt`. **No payloads**.
- `newStepDetailView` — `GetStep` (api-3.md §10.2.2): the same plus `inputPayload`, `outputPayload`, `errorPayload`.

`ListSteps` → summary view; `GetStep` → detail view. The two are kept adjacent so the divergence stays visible and intentional.

## TDD & Verification

| Gate | Result |
|---|---|
| `TestHandler_StepResponseShape` extended (GetStep asserts `*Payload` keys present + short keys absent; ListSteps asserts no payload keys) | ✅ green |
| Mutation: revert detail keys to `input`/`output`/`error` | ✅ `TestHandler_StepResponseShape/GetStep` FAILS |
| `grep inputPayload/outputPayload/errorPayload handler.go` | ✅ present in GetStep's view (lines 289–291), absent from ListSteps's summary view |
| `make ci` | ✅ exit 0 |
| `FLOWFORGE_INTEGRATION=1 make test-integration` | ✅ exit 0 |

## Notes for Phase 7

- Carried forward: diff every endpoint's JSON against the spec's literal example blocks — the plan's *sample code* was the second source of a field-name mismatch in two rounds. Whoever writes fix example code must check the spec too.
- The `idempotency_keys` table's continued disuse still needs an explicit decision before Phase 7.

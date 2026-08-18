# FlowForge — Remaining Backlog

**Status:** Extracted from [backlog.md](../docs/backlog.md), 2026-08-06 — scoped to what has **not** been executed.
**Purpose:** A standalone reference for what's left, with current status verified against the actual codebase (not assumed), so this list doesn't drift from reality the way the master backlog's own carried-forward notes occasionally did across this project's execution history.

---

## Completed — not repeated here in detail

Phases 0–8 and 12 (backend infrastructure through real-time monitoring, plus testing/reliability) are executed, reviewed, and verified live against real Postgres/Redis/NATS. All carried-forward technical debt accumulated across those phases (rate limiting, CORS, security headers, `/metrics`, audit logging gaps, the `idempotency_keys` cleanup, `ContextWithTx` widening, the Case A trigger listener, and its `triggerType` clamp fix) is closed. See `.agents/memory/action_history.md` for the full execution record and `.agents/plans/phase_*.md` for each phase's individual plan and review trail.

Two items remain **structurally open regardless of any future phase**, carried forward rather than assigned to a phase below:

1. **`internal/platform/eventbus`'s wire contract is a documented placeholder** (Phase 7, decision D-4) — the generic gRPC/NATS contract works and is tested against itself, but matching a real external system's actual proto/subject naming needs that system's spec, which no phase in this project can supply on its own.
2. **CORS's actual allowed-origin value** is unset (empty allowlist = same-origin only) until Phase 9 below produces a real frontend origin to configure.

---

## Phase 9 — Workflow Builder Frontend

### Goal
Provide a usable UI for workflow creation and management.

### Scope
- App shell
- Login page
- Workflow list page
- Workflow detail page
- Workflow editor page
- Draft save flow
- Publish and rollback actions

### Done When
- A user can create and manage workflows from the UI.
- The UI reflects workflow version state correctly.

### Status
Not started. **Backend is fully ready for this phase** — every endpoint this UI needs already exists and is tested: auth/login (Phase 2), workflow CRUD + draft save + publish/rollback (Phase 3), and the `httpx` envelope every response already follows consistently. No backend work is a prerequisite.

### Notes for planning
- This is the phase that finally supplies a real CORS origin (see the carried-forward item above) — the `internal/platform/httpmw.CORS` middleware exists and is wired, only its configured origin list is still empty.
- Framework/tooling choice (React/Vue/htmx/etc.), state management, and the workflow graph editor's rendering approach (canvas vs. DOM-based node graph) are all open decisions with no precedent elsewhere in this codebase — worth resolving explicitly before implementation, the same way LLM-provider and event-transport choices were resolved earlier in this project rather than assumed.

---

## Phase 10 — Execution Monitoring Frontend

### Goal
Provide a clear visual experience for workflow runs.

### Scope
- Run history page
- Run detail page
- Step state visualization
- Execution log viewer
- Live status updates
- Basic health dashboard

### Done When
- A user can inspect workflow runs and logs.
- The UI updates in real time during execution.
- The dashboard shows basic operational health.

### Status
Not started. **Backend is fully ready** — run/step/log listing (Phase 6), and the live-update mechanism this phase needs (SSE at `GET /api/v1/events`, Phase 8) both already exist and are tested, including the multi-instance Redis Pub/Sub fan-out.

### Notes for planning
- "Basic health dashboard" — the backend's `/health` (both processes) and `/metrics` (both processes, Prometheus format) endpoints exist; this phase's dashboard is presentation over data that's already exposed, not new backend surface.
- Depends on Phase 9 only for shared UI shell/auth (app shell, login) — the two frontend phases could plausibly be built as one coherent app rather than strictly sequenced, if that's a better fit than the backlog's phase-by-phase framing once frontend work starts.

---

## Phase 11 — AI Feature (partially complete)

### Goal
Add one meaningful AI-powered feature.

### Scope
- ~~AI failure analysis for failed workflow runs (`POST /api/v1/workflow-runs/{runId}/analysis`)~~ **done** — built in Phase 6
- ~~Redaction of sensitive headers/keys/payloads before calling LLM~~ **done** — `internal/platform/redact`, Phase 6
- ~~Strict JSON output schema validation (`diagnosis`, `possibleCause`, `suggestedFix`, `confidence`)~~ **done** — Phase 6
- **Read-only UI presentation on run detail page** — not started

### Done When
- The AI failure analysis feature is visible in the run detail UI. ← **only remaining criterion**
- Output is strictly validated against the JSON schema. — done
- Sensitive data is never sent to the model. — done

### Status
Backend fully shipped (Phase 6, DeepSeek provider, reviewed and tested including a planted-secret redaction proof). Only the UI panel remains, and it depends on Phase 10's run detail page existing first — there is nowhere to place a "read-only AI analysis" panel before that page is built.

---

## Phase 13 — CI, Documentation, and Portfolio Polish

### Goal
Make the repository presentable and easy to evaluate.

### Scope
- CI pipeline
- README
- Trade-offs section
- Architecture overview
- Review exercise file
- Demo data
- Demo screenshots or short recording

### Done When
- Pull requests are validated automatically.
- The repository is easy to understand.
- The project is ready to be shown as a portfolio piece.

### Status
Not started. **Confirmed via direct check**: no `.github/workflows/` directory exists at all — zero automated CI today, despite `make ci` (fmt-check, vet, build, test -race) already being the exact, ready-to-wrap command. `README.md` is 14 lines — prerequisites and a build command only, no trade-offs section, no architecture overview (though `.agents/docs/ARCHITECTURE.md` exists internally and could be adapted rather than written from scratch).

### Notes for planning — this phase is not frontend-gated
Unlike Phases 9–11, most of this phase has **no dependency on frontend work existing**:

| Item | Frontend-dependent? |
|---|---|
| CI pipeline (wrap `make ci` in GitHub Actions) | No — can be done today |
| README expansion, trade-offs section, architecture overview | No — can be done today |
| Review exercise file | Likely no, depending on what it's meant to exercise |
| Demo data / seed | No — backend-only, `migrations/seed.sql` already exists as a starting point |
| Demo screenshots or recording | **Yes** — needs Phase 9/10's UI to exist first |

Only the last item is genuinely blocked. The rest could be pulled forward and done in parallel with, or even before, Phase 9 — flagged here since the natural reading of "Phase 13 comes last" undersells how little of it actually depends on that ordering.

---

## Recommended Order (revised from the master backlog's own suggestion, given verified status)

The master `backlog.md`'s "Suggested Execution Order" places CI/Portfolio Polish strictly last. Given what's now verified:

1. **Phase 9** — Workflow Builder Frontend (no blockers)
2. **Phase 10** — Execution Monitoring Frontend (no blockers beyond sharing Phase 9's shell)
3. **Phase 11's remaining UI panel** (needs Phase 10's run detail page)
4. **Phase 13** — but its non-screenshot items (CI pipeline, docs) can run **in parallel with 9–11** rather than waiting, since they share no files or dependencies with frontend work. Only "demo screenshots/recording" is a true final step, once 9–11 exist to screenshot.

This isn't a mandate to reorder the backlog — it's this document's job to make the *actual* dependency graph visible, since it's looser than the phase numbers alone suggest.

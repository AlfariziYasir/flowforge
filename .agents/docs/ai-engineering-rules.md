# FlowForge — AI Engineering Rules

**Status:** Draft v1
**Purpose:** Define vendor-agnostic rules for how any AI coding agent must work on FlowForge.
**Audience:** Human reviewers, AI coding agents, and implementation assistants.

This document is intentionally **tool-agnostic**. It does not assume any specific model, CLI, or editor. It defines the engineering behavior expected from any AI used to help build FlowForge.

---

## 1. Purpose

This document exists to make AI-assisted development predictable, safe, and consistent.

Its goals are to:

- keep implementation aligned with the frozen design documents,
- prevent AI from inventing new architecture or changing contracts,
- make AI output reviewable by a human,
- preserve testability, safety, and maintainability,
- and reduce rework during implementation.

---

## 2. Project Context

FlowForge is a multi-tenant workflow orchestration platform inspired by Zapier and n8n.

The frozen design documents are:

- `prd.md`
- `architecture.md`
- `database.md`
- `api-1.md`
- `api-2.md`
- `api-3.md`
- `api-4.md`
- `backlog.md`

The implementation is expected to follow:

- modular monolith architecture,
- separate API and worker processes,
- PostgreSQL as source of truth,
- Redis for queue/coordination,
- TDD-first development,
- safe concurrency patterns,
- and one AI-assisted product feature.

---

## 3. Engineering Principles

Any AI agent working on FlowForge must follow these principles:

1. **Contract first** — implement only what is defined in the frozen documents.
2. **TDD first** — write or update tests before implementation when feasible.
3. **Small changes** — prefer narrow, reviewable changes over broad refactors.
4. **Deterministic behavior** — avoid hidden side effects and unpredictable runtime behavior.
5. **Tenant safety** — never weaken tenant boundaries.
6. **Concurrency safety** — never introduce race-prone patterns.
7. **Immutable history** — preserve workflow run history and execution logs.
8. **Explicit trade-offs** — if a compromise is necessary, document it clearly.
9. **Reviewability** — every AI change must be understandable by a human reviewer.
10. **No silent scope expansion** — do not add features outside the task.

---

## 4. Decision Hierarchy

When there is a conflict, the following order of authority applies:

```text
PRD
  ↓
Architecture
  ↓
Database Design
  ↓
API Specification
  ↓
AI Engineering Rules
  ↓
Task Specification
  ↓
Implementation
```

Rules:

- AI must not override anything in a higher-level document.
- If a conflict exists, stop and report it.
- If a task is ambiguous, do not guess silently.
- If a better approach exists but changes the contract, it requires human approval.

---

## 5. AI Behavior Rules

### 5.1 Read before acting

Before making changes, the AI must read the relevant task and the related design documents.

### 5.2 Plan before editing

The AI should produce a short implementation plan before modifying code when the task is non-trivial.

### 5.3 Stay within scope

The AI must only implement what the task requires.
Do not add unrelated refactors, dependencies, endpoints, or abstractions.

### 5.4 Preserve existing behavior

Do not break existing flows unless the task explicitly requires it.

### 5.5 Ask when blocked

If required information is missing or conflicting, the AI must stop and ask for clarification.

### 5.6 Prefer reversibility

When possible, changes should be easy to revert and isolate.

### 5.7 Make assumptions explicit

If a small assumption is necessary, state it clearly in the output.

---

## 6. Architecture Rules

### 6.1 Modular monolith boundary

- The codebase remains a modular monolith.
- API and worker are separate runtime entrypoints.
- Internal packages must respect domain boundaries.

### 6.2 No architecture drift

The AI must not:

- introduce microservices,
- move business logic into controllers,
- create unnecessary shared global state,
- or add new infrastructure components not present in the design.

### 6.3 Separation of concerns

- HTTP handlers should remain thin.
- Business logic belongs in services/use cases.
- Execution coordination belongs in the worker coordinator layer.
- Step execution belongs in executors.
- Persistence belongs in repositories.

### 6.4 Engine purity

The workflow engine should not depend on HTTP, Redis implementation details, or UI concerns.

### 6.5 Boundary protection

If the task touches a boundary, keep the boundary explicit and testable.

---

## 7. Coding Rules

### 7.1 General code quality

- Keep code readable and minimal.
- Use clear names.
- Avoid over-abstraction.
- Prefer simple functions and explicit flows.

### 7.2 Go code rules

- Use idiomatic Go.
- Keep functions focused.
- Avoid unbounded goroutines.
- Use `context.Context` for all cancellation-aware flows.
- Always handle errors explicitly.
- Avoid `panic` except for unrecoverable startup failures.

### 7.3 Dependency rules

- Do not add dependencies unless necessary.
- Prefer standard library and already-approved libraries.
- Any new dependency must be justified.

### 7.4 Public interface rules

- Do not change public API or exported domain behavior without an approved task.
- Do not rename public contracts casually.

### 7.5 Error handling rules

- Errors must be meaningful and actionable.
- Convert internal errors into stable domain or API errors where appropriate.
- Do not leak sensitive internal details to clients.

---

## 8. TDD Workflow

### 8.1 Default workflow

1. Read task and related documents.
2. Identify expected behavior.
3. Write or update tests first when practical.
4. Implement the smallest change that passes.
5. Refactor without changing behavior.
6. Re-run tests.
7. Verify quality gates.

### 8.2 When TDD is mandatory

TDD is mandatory for:

- workflow validation,
- topological sort,
- execution scheduling,
- retry logic,
- timeout logic,
- locking behavior,
- tenant isolation,
- API contract behavior,
- and any concurrency-sensitive code.

### 8.3 When TDD may be partial

For small mechanical refactors, test-first may not be practical, but regression tests must still be added when behavior changes.

### 8.4 Bug fixes

Every bug fix must include a regression test that would fail before the fix.

---

## 9. Testing Rules

### 9.1 Test layers

- **Unit tests** for pure logic.
- **Repository tests** for persistence and tenant filtering.
- **Integration tests** for API, DB, queue, and worker interactions.
- **End-to-end tests** for a full workflow run.
- **Concurrency tests** for race-prone flows.

### 9.2 Mandatory coverage areas

- DAG validation
- cycle detection
- topological sorting
- workflow publish/rollback
- workflow trigger idempotency
- run cancellation and retry
- worker claim logic
- SSE event emission
- authentication and RBAC
- tenant isolation

### 9.3 Race detection

Concurrency-sensitive changes must be validated with race detection when possible.

### 9.4 Test stability

- Tests must be deterministic.
- Avoid flaky timing-based assertions.
- Use fakes or controlled clocks where practical.

### 9.5 Test ownership

The AI must not remove or weaken tests to make code pass.

---

## 10. Concurrency Rules

### 10.1 Goroutine safety

- Every goroutine must have a clear owner.
- Every goroutine must be tied to a cancelable context when appropriate.
- Every background loop must stop on context cancellation.
- Channels must have explicit closing ownership.

### 10.2 Bounded parallelism

- Use bounded worker pools or equivalent controls.
- Never spawn unbounded goroutines for workflow steps.

### 10.3 Shared state

- Avoid shared mutable state when possible.
- If shared state is necessary, protect it explicitly.
- Prefer database state over in-memory mutable state for durable workflow execution.

### 10.4 Claim safety

- Use atomic claim logic for runs and steps.
- Never allow duplicate execution due to concurrent workers.

### 10.5 Cancellation and timeout

- Cancellation must propagate through the execution chain.
- Timeouts must be enforced at workflow and step levels where applicable.

---

## 11. Database Rules

### 11.1 Follow the frozen schema

- Do not add new tables or columns unless the task requires it.
- Do not change aggregate boundaries without approval.
- Do not weaken constraints.

### 11.2 Tenant isolation

- Every tenant-owned query must include tenant scope.
- Never rely on client input for tenant identity.

### 11.3 Transaction boundaries

- Keep transactions short.
- Do not hold transactions open during long-running execution.
- Use a transaction only for a single business operation.

### 11.4 Locking strategy

- Use optimistic locking for editable workflow metadata.
- Use pessimistic/atomic claim for workflow runs and step runs.
- Respect idempotency constraints.

### 11.5 Data integrity

- Preserve immutability of workflow versions and execution history.
- Do not update execution logs as if they were mutable application state.

---

## 12. API Rules

### 12.1 Follow API contract

- Do not invent endpoints.
- Do not change request or response formats without approval.
- Follow the lifecycle-based API design.

### 12.2 Status codes

- Use the documented HTTP status codes.
- Do not replace `202 Accepted` with `200 OK` when the API only enqueues work.

### 12.3 Error envelope

- Use the standard error envelope.
- Return stable error codes.
- Do not leak stack traces.

### 12.4 Idempotency

- Respect `Idempotency-Key` where required.
- Deduplicate logically equivalent create/retry operations.

### 12.5 Security and tenant checks

- Every protected route must enforce authentication and authorization.
- Every resource access must be tenant-scoped.

---

## 13. Security Rules

### 13.1 Secrets

- Never log secrets.
- Never send secrets to AI prompts.
- Redact sensitive values before persistence to logs or analysis artifacts.

### 13.2 SSRF protection

- HTTP execution must block localhost and private network targets.
- Enforce request timeouts and response size limits.

### 13.3 Input hygiene

- Validate JSON inputs.
- Reject invalid enums, invalid UUIDs, malformed payloads, and unsupported shapes.

### 13.4 Least privilege

- Use the minimum privileges needed for each operation.
- Do not grant wider access just to simplify implementation.

---

## 14. Documentation Rules

### 14.1 Keep docs aligned

If implementation behavior changes, update the relevant document or task notes.

### 14.2 No undocumented behavior

Do not leave behavior undocumented if it affects other developers or AI agents.

### 14.3 Explain trade-offs

When making a non-obvious implementation choice, document the reason.

---

## 15. Refactoring Rules

### 15.1 Refactor only with purpose

Refactor only when it improves readability, testability, or correctness.

### 15.2 Avoid scope creep

Do not refactor unrelated code just because it is nearby.

### 15.3 Preserve contracts

Refactoring must not change public behavior unless explicitly requested.

### 15.4 Refactor after tests pass

If the task is functional, verify behavior first, then refactor safely.

---

## 16. Implementation Workflow

The recommended AI workflow is:

1. Read task and context.
2. Summarize the goal.
3. Identify affected files.
4. Write or update tests.
5. Implement the change.
6. Run validation checks.
7. Refactor if needed.
8. Summarize outcome and remaining work.

If a task is too large, split it into smaller tasks before implementation.

---

## 17. Output Format

Every AI implementation response should ideally include:

- **Summary**
- **Plan**
- **Files Changed**
- **Implementation Notes**
- **Tests Added / Updated**
- **Validation**
- **Risks / Trade-offs**
- **Remaining Work**

This format makes review easier and keeps AI output consistent across different tools.

---

## 18. Quality Gates

A task cannot be considered complete unless the following are satisfied:

- build succeeds,
- tests pass,
- relevant new tests were added,
- formatting is correct,
- linting passes when applicable,
- race-sensitive code has been reviewed for concurrency safety,
- API and database contracts are respected,
- documentation is updated if behavior changed.

For Go code, the following should be checked when relevant:

- `gofmt`
- `go test`
- `go test -race`
- `golangci-lint`

---

## 19. Forbidden Actions

An AI agent must not:

- change architecture without approval,
- change database schema without a task,
- change API contracts without approval,
- introduce unapproved dependencies,
- remove required tests,
- suppress failing tests,
- disable race detection to hide issues,
- bypass locking or idempotency rules,
- expose secrets,
- rewrite unrelated modules for convenience,
- or silently expand scope.

---

## 20. Collaboration Rules

### 20.1 Human is final owner

The human reviewer makes the final decision.

### 20.2 AI is an assistant

AI may propose, implement, and refactor within the approved contract, but not redefine the project.

### 20.3 Single source of truth

All AI agents must treat the frozen design documents and task specification as the single source of truth.

### 20.4 Multi-AI compatibility

These rules must work with any coding agent, editor integration, or CLI-based assistant.

---

## 21. Definition of Done

An AI-generated task is done only when:

- it matches the approved task scope,
- it respects architecture, database, and API contracts,
- tests are added or updated,
- quality gates are met,
- no forbidden action was taken,
- and the output is reviewable by a human.

---

## 22. Final Notes

This document is intentionally strict.

Strict rules are useful because FlowForge is not a toy project. It is a workflow orchestration engine with concurrency, multi-tenancy, execution history, and AI-assisted behavior.

The more precise the rules, the more reliable the AI-assisted implementation will be.

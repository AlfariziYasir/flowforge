# FlowForge — API Specification v1

**Status:** Draft v1
**Scope:** Part 4 of 4 — Platform & Engineering Contract
**Purpose:** Define the platform-level API contract for SSE, security, versioning, lifecycle rules, contract testing, observability, performance targets, and future evolution.

> This document follows the contract style established in Parts 1–3.
> It completes the API contract and closes the Design Phase.

---

## 11. Server-Sent Events (SSE)

### 11.1 Goals

The SSE API provides near real-time updates for workflow execution without requiring polling.

Supported use cases:

- workflow run lifecycle updates
- step lifecycle updates
- log append notifications
- dashboard live status updates
- future AI progress notifications

### 11.2 Endpoint

```http
GET /api/v1/events
```

### 11.3 Authentication

Required.

Headers:

```http
Authorization: Bearer <access_token>
Accept: text/event-stream
```

### 11.4 Connection Rules

- One SSE connection per authenticated client is sufficient for MVP.
- Connections are tenant-scoped.
- Events must never leak across tenants.
- The server should support automatic reconnection from the client.
- Heartbeats should keep idle connections alive.

### 11.5 Event Format

SSE messages must use the standard SSE format.

Example:

```text
event: workflow.run.started
id: 123456
data: {"runId":"uuid","workflowId":"uuid","status":"running","timestamp":"2026-01-02T00:00:00Z"}
```

### 11.6 Supported Events

#### Workflow run events

- `workflow.run.created`
- `workflow.run.queued`
- `workflow.run.started`
- `workflow.run.completed`
- `workflow.run.failed`
- `workflow.run.cancelRequested`
- `workflow.run.cancelled`
- `workflow.run.retryRequested`

#### Step events

- `step.started`
- `step.completed`
- `step.failed`
- `step.retrying`

#### AI and system events

- `workflow.analysis.completed`
- `heartbeat`

### 11.7 Reconnection Strategy

- Clients should reconnect automatically.
- The `Last-Event-ID` header should be supported when practical.
- The server should tolerate transient disconnects without affecting execution.

### 11.8 Heartbeat

Recommended heartbeat interval: 30 seconds.

Example:

```text
event: heartbeat
data: {}
```

### 11.9 Ordering Guarantees

- Ordering is guaranteed only within the same workflow run.
- No global ordering guarantee exists across multiple runs.

### 11.10 Failure Handling

- If SSE disconnects, the client should reconnect.
- Worker execution continues independently.
- API must never restart workflow execution because of SSE disconnects.

---

## 12. Security

### 12.1 Authentication

- JWT authentication is required for protected endpoints.
- Bearer token must be present in the Authorization header.
- Access tokens are short-lived.
- Refresh token support is part of the auth contract.

### 12.2 Authorization

- Role-based access control is enforced on every protected route.
- Supported roles: `admin`, `editor`, `viewer`.

### 12.3 Tenant isolation

- Every request is executed within one tenant.
- Tenant ID must come from the JWT, not from user input.
- Cross-tenant access is forbidden.

### 12.4 Input validation

All request bodies must be validated.
Reject:

- unknown fields when strict mode is enabled,
- invalid UUID values,
- invalid enums,
- invalid pagination values,
- invalid JSON payloads.

### 12.5 Payload validation

Workflow graph payloads must be validated for:

- DAG structure,
- cycle detection,
- required node fields,
- valid edge references.

AI responses must be validated for:

- schema correctness,
- required fields,
- confidence range if used.

### 12.6 Secret redaction

Sensitive values must never appear in:

- API responses,
- execution logs,
- AI prompts,
- audit logs.

Examples of sensitive data:

- API keys
- passwords
- access tokens
- refresh tokens
- authorization headers

### 12.7 Idempotency

Required for:

- workflow trigger
- workflow retry
- any future request that can create duplicate side effects

Header:

```http
Idempotency-Key: <opaque_key>
```

Duplicate requests must return or map to the existing logical operation result.

### 12.8 SSRF protection

HTTP task execution must enforce:

- blocking of localhost,
- blocking of private IP ranges,
- blocking of link-local metadata endpoints,
- timeouts,
- response size limits.

### 12.9 HTTP security headers

Recommended headers:

- `X-Content-Type-Options`
- `X-Frame-Options`
- `Referrer-Policy`
- `Content-Security-Policy`

### 12.10 CORS

- Allowed origins must be configurable.
- Credentials should be disabled by default unless explicitly required.

### 12.11 Rate limiting

Recommended MVP limits:

- Authentication API: 10 requests/minute
- Workflow trigger: 30 requests/minute
- Read APIs: 120 requests/minute

### 12.12 Audit logging

Sensitive actions must create audit log records.
Examples:

- login
- user created
- user updated
- workflow published
- workflow deleted
- workflow triggered
- workflow cancelled
- workflow retried

---

## 13. API Versioning Strategy

### 13.1 Versioning method

Use URI versioning.

```http
/api/v1
```

### 13.2 Breaking changes

Breaking changes require a new version path, such as `/api/v2`.

### 13.3 Non-breaking changes

Allowed without version bump:

- optional fields,
- new endpoints,
- additional response metadata,
- documentation improvements.

### 13.4 Deprecation policy

Deprecated endpoints remain available until the replacement is documented and adopted.

### 13.5 Sunset policy

Deprecated APIs should announce removal before deletion.

---

## 14. API Lifecycle

API endpoints move through the following lifecycle:

```text
Experimental -> Beta -> Stable -> Deprecated -> Removed
```

Only Stable APIs are guaranteed for production clients.

Recommended guidance:

- use Experimental only for internal or changing capabilities,
- keep MVP endpoints stable whenever possible,
- avoid exposing multiple competing endpoint styles.

---

## 15. Contract Testing Strategy

Every endpoint must be supported by tests that reflect the contract.

### 15.1 Unit test

Tests business logic and validation functions.

### 15.2 Handler test

Tests HTTP layer behavior, status codes, request parsing, and response shaping.

### 15.3 Service test

Tests application-level orchestration and business rules.

### 15.4 Repository test

Tests persistence behavior, tenant filtering, and locking rules.

### 15.5 Integration test

Tests the full flow involving API, database, queue, and worker interactions where relevant.

### 15.6 Contract test

Verifies request validation, response schema, and error envelope consistency.

### 15.7 Concurrency test

Use race detection for concurrency-sensitive paths.

```bash
go test -race
```

### 15.8 Benchmark

Critical endpoints and engine paths should be benchmarked when useful.

---

## 16. Observability

### 16.1 Request ID

Every request should carry or receive a request ID.

### 16.2 Correlation ID

Correlation should be shared across:

- API request,
- workflow run,
- worker execution,
- logs,
- SSE events.

### 16.3 Structured logging

Use structured JSON logs with fields such as:

- `requestId`
- `correlationId`
- `runId`
- `stepRunId`
- `workerId`
- `tenantId`

### 16.4 Metrics

Recommended metrics:

- API request count
- API latency
- active workers
- workflow runs
- step runs
- queue length
- retry count
- failed runs

### 16.5 Tracing

OpenTelemetry tracing is a future enhancement, not required for MVP.

---

## 17. Performance Targets

| Metric                      |                               Target |
| --------------------------- | -----------------------------------: |
| API latency P95             | < 200 ms for non-execution endpoints |
| Trigger workflow acceptance |                             < 300 ms |
| SSE delivery delay          |                           < 1 second |
| Pagination default          |                             20 items |
| Pagination maximum          |                            100 items |
| Request body limit          |                                 1 MB |
| Worker scaling              |                           Horizontal |

These targets are engineering goals, not production SLAs.

---

## 18. Future Evolution

Possible future enhancements:

- webhooks,
- OAuth integrations,
- public API keys,
- GraphQL,
- gRPC,
- WebSocket,
- multi-region deployment,
- stronger tenant isolation with PostgreSQL RLS,
- dedicated execution log store,
- richer AI-assisted workflow creation.

---

## 19. Definition of Done

The API Specification is complete when:

- the API contract is fully documented,
- OpenAPI generation is possible,
- authentication and authorization are defined,
- error handling is consistent,
- SSE is documented,
- security rules are explicit,
- versioning strategy is documented,
- contract testing strategy is documented,
- observability and performance targets are documented,
- future evolution is documented,
- and the Design Phase can be considered frozen.

---

## 20. Final Status

**API Specification v1: FINAL**
**Design Phase: COMPLETE**
**Next Phase: Engineering Rules, Task Specification, and Implementation**

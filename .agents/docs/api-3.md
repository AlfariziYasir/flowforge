# FlowForge — API Specification v1

**Status:** Draft v1
**Scope:** Part 3 of 4 — Execution Lifecycle API
**Purpose:** Define the API contract for workflow run execution, step run inspection, execution logs, lifecycle actions, and AI-assisted run analysis.

> This document follows the contract style established in Part 1 and Part 2.
> All responses use the standard envelope unless otherwise noted.
> The API reflects the execution lifecycle, not raw database tables.

---

## 10. Execution Lifecycle API

---

## 10.1 Workflow Run API

Workflow runs represent execution instances created from a published workflow version. They are write-once, append-only execution records after creation.

### 10.1.1 POST /api/v1/workflows/{workflowId}/runs

### Summary

Trigger a workflow run for the current tenant.

### Description

This endpoint validates the workflow and its published version, creates a workflow run record, deduplicates via idempotency when provided, and enqueues the run for background execution by the worker.

The API does **not** execute the workflow directly. It returns after the run is accepted.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Headers

* `Authorization: Bearer <access_token>`
* `Content-Type: application/json`
* `Idempotency-Key: <opaque_key>` recommended
* `X-Request-Id: <request_id>` optional

### Path Parameters

* `workflowId` — UUID of the workflow

### Request Body

```json
{
  "input": {
    "leadEmail": "user@example.com",
    "leadName": "Jane Doe"
  },
  "triggerSource": "manual"
}
```

### Validation Rules

* `workflowId` must be a valid UUID.
* `input` is optional but must be valid JSON if provided.
* `triggerSource` must be `manual` for MVP if explicitly supplied.
* Idempotency key, when present, must be treated as tenant-scoped.

### Business Rules

* The workflow must belong to the authenticated tenant.
* The workflow must have a published active version.
* The workflow run must reference exactly one workflow version.
* The API must not block on worker execution.
* Duplicate trigger requests with the same idempotency key must not create duplicate runs.

### Side Effects

* Create a workflow run record.
* Create an idempotency key record when applicable.
* Enqueue a job for the worker.
* Write audit log entry.
* Publish run-created and run-queued events.

### Database Operations

* Read from `workflows`
* Read from `workflow_versions`
* Insert into `workflow_runs`
* Insert into `idempotency_keys` when used
* Insert into `audit_logs`
* Potentially update workflow metadata if needed for trigger bookkeeping

### Events Published

* `workflow.run.created`
* `workflow.run.queued`

### Execution Flow

```text
HTTP Request
  -> Authenticate
  -> Authorize
  -> Load workflow
  -> Validate published version exists
  -> Validate idempotency
  -> Create workflow_run
  -> Persist idempotency key
  -> Enqueue job
  -> Publish SSE/event notification
  -> Return 202 Accepted
```

### Success Response

```json
{
  "success": true,
  "data": {
    "runId": "uuid",
    "workflowId": "uuid",
    "workflowVersionId": "uuid",
    "status": "pending",
    "triggerType": "manual",
    "createdAt": "2026-01-02T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `403 AUTH_FORBIDDEN`
* `404 WORKFLOW_NOT_FOUND`
* `409 RUN_ALREADY_RUNNING` — if deduplication or policy blocks duplicate execution
* `409 WORKFLOW_VERSION_CONFLICT` — if workflow state changes conflict with run creation
* `422 VALIDATION_ERROR`
* `429 RATE_LIMITED`

### Acceptance Criteria

* A valid request creates one workflow run.
* The run is tenant-scoped.
* The API returns `202 Accepted`.
* The workflow is not executed synchronously in the API process.
* Idempotent retry does not create duplicate runs.

### TDD Test Scenarios

* trigger run success
* trigger run without published version
* trigger run forbidden for viewer
* duplicate idempotency key
* tenant isolation
* validation error

### Example Request

```json
{
  "input": {
    "leadEmail": "user@example.com",
    "leadName": "Jane Doe"
  },
  "triggerSource": "manual"
}
```

### Example Response

```json
{
  "success": true,
  "data": {
    "runId": "8a3c3d8d-4d4d-4d74-9c4d-9f4e2a3d0a11",
    "workflowId": "2fd7a1e1-82d8-4d26-bd87-7f4f0f33a111",
    "workflowVersionId": "d19d0f12-4e64-4d8c-a4f7-1e9fd5a0b222",
    "status": "pending",
    "triggerType": "manual",
    "createdAt": "2026-01-02T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

---

### 10.1.2 GET /api/v1/workflows/{workflowId}/runs

### Summary

List workflow runs for a specific workflow.

### Description

Returns run history for a workflow with pagination and filtering.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

* `workflowId`

### Query Parameters

* `page`
* `pageSize`
* `status` optional
* `triggerType` optional
* `createdAtFrom` optional
* `createdAtTo` optional

### Business Rules

* Results must be tenant-scoped.
* Viewer can read run history.
* Sorting should default to newest first.

### Database Operations

* Read from `workflow_runs`

### Events Published

* None

### Success Response

```json
{
  "success": true,
  "data": {
    "items": [
      {
        "runId": "uuid",
        "workflowId": "uuid",
        "workflowVersionId": "uuid",
        "status": "succeeded",
        "triggerType": "manual",
        "startedAt": "2026-01-02T00:00:00Z",
        "finishedAt": "2026-01-02T00:00:05Z",
        "createdAt": "2026-01-02T00:00:00Z"
      }
    ],
    "pagination": {
      "page": 1,
      "pageSize": 20,
      "totalItems": 1,
      "totalPages": 1,
      "hasNext": false,
      "hasPrev": false
    }
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `404 WORKFLOW_NOT_FOUND`
* `403 AUTH_FORBIDDEN`

### Acceptance Criteria

* Returns paginated runs for the workflow.
* Results are tenant-scoped.
* Filters are applied correctly.

### TDD Test Scenarios

* list runs success
* filter by status
* filter by date range
* tenant isolation
* pagination

---

### 10.1.3 GET /api/v1/workflow-runs/{runId}

### Summary

Get a workflow run summary and lifecycle status.

### Description

Returns the current state of a workflow run, including timing, status, and related metadata.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

* `runId`

### Business Rules

* The run must belong to the authenticated tenant.
* The response must not expose secrets or raw sensitive payloads.

### Database Operations

* Read from `workflow_runs`
* Read from `workflows`
* Read from `workflow_versions`

### Events Published

* None

### Success Response

```json
{
  "success": true,
  "data": {
    "runId": "uuid",
    "workflowId": "uuid",
    "workflowVersionId": "uuid",
    "status": "running",
    "triggerType": "manual",
    "startedAt": "2026-01-02T00:00:00Z",
    "finishedAt": null,
    "createdAt": "2026-01-02T00:00:00Z",
    "updatedAt": "2026-01-02T00:00:02Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `404 RUN_NOT_FOUND`
* `403 AUTH_FORBIDDEN`

### Acceptance Criteria

* Returns current run state.
* Tenant isolation is enforced.

### TDD Test Scenarios

* get run success
* run not found
* tenant isolation

---

### 10.1.4 POST /api/v1/workflow-runs/{runId}/cancel

### Summary

Request cancellation of a workflow run.

### Description

The run is marked for cancellation. The worker should stop execution as soon as safely possible.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Path Parameters

* `runId`

### Headers

* `Idempotency-Key: <opaque_key>` recommended

### Business Rules

* Cancellation should be safe and idempotent.
* If the run is already terminal, cancel should not restart it.
* The worker should observe cancellation via run state or cancellation signal.

### Side Effects

* Update run state to a cancellation-related state if allowed.
* Write audit log.
* Publish cancellation event.

### Database Operations

* Update `workflow_runs`
* Insert into `audit_logs`

### Events Published

* `workflow.run.cancelRequested`
* `workflow.run.cancelled` when terminal cancellation completes

### Execution Flow

```text
HTTP Request
  -> Authenticate
  -> Authorize
  -> Load run
  -> Validate tenant
  -> Check terminal state
  -> Mark cancellation requested
  -> Write audit log
  -> Publish event
  -> Return 202 Accepted
```

### Success Response

```json
{
  "success": true,
  "data": {
    "runId": "uuid",
    "status": "cancelling"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `403 AUTH_FORBIDDEN`
* `404 RUN_NOT_FOUND`
* `409 RUN_ALREADY_COMPLETED`
* `409 RUN_ALREADY_RUNNING` — only if policy disallows cancellation in current state

### Acceptance Criteria

* Cancellation request is accepted for an active run.
* Terminal runs are not cancelled again.

### TDD Test Scenarios

* cancel running run
* cancel completed run
* tenant isolation
* forbidden access

---

### 10.1.5 POST /api/v1/workflow-runs/{runId}/retry

### Summary

Retry a failed or canceled workflow run.

### Description

Creates a new execution attempt from the same workflow version, preserving history of the prior run.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Path Parameters

* `runId`

### Headers

* `Idempotency-Key: <opaque_key>` recommended

### Request Body

```json
{
  "retryFailedStepsOnly": false
}
```

### Validation Rules

* `retryFailedStepsOnly` is optional and defaults to `false`.

### Business Rules

* Only retryable runs may be retried.
* Retry should create a new run record or a new run attempt according to implementation policy.
* Historical run data must remain immutable.
* Previous outputs should not be overwritten.

### Side Effects

* Create a new run attempt or cloned run.
* Write audit log.
* Enqueue new job.
* Publish retry event.

### Database Operations

* Read from `workflow_runs`
* Read from `step_runs`
* Insert into `workflow_runs`
* Insert into `audit_logs`
* Insert into `idempotency_keys` if used

### Events Published

* `workflow.run.retryRequested`
* `workflow.run.queued`

### Execution Flow

```text
HTTP Request
  -> Authenticate
  -> Authorize
  -> Load original run
  -> Validate retry eligibility
  -> Create retry run
  -> Copy or reference execution context
  -> Enqueue new run job
  -> Publish event
  -> Return 202 Accepted
```

### Success Response

```json
{
  "success": true,
  "data": {
    "runId": "uuid",
    "originalRunId": "uuid",
    "status": "pending"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `404 RUN_NOT_FOUND`
* `409 RUN_ALREADY_COMPLETED`
* `409 RUN_ALREADY_RUNNING`
* `403 AUTH_FORBIDDEN`

### Acceptance Criteria

* Failed run can be retried safely.
* Retry does not overwrite historical run data.
* New run is tenant-scoped.

### TDD Test Scenarios

* retry failed run
* retry terminal run
* retry with idempotency key
* forbidden retry

---

## 10.2 Step Run API

Step runs are immutable execution records. They are read-only from the public API.

### 10.2.1 GET /api/v1/workflow-runs/{runId}/steps

### Summary

List all step runs for a workflow run.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

* `runId`

### Business Rules

* Step runs are returned in execution order or stable node order.
* Tenant isolation must be enforced.

### Database Operations

* Read from `step_runs`

### Events Published

* None

### Success Response

```json
{
  "success": true,
  "data": {
    "items": [
      {
        "stepRunId": "uuid",
        "workflowNodeId": "uuid",
        "nodeKey": "send_to_crm",
        "status": "succeeded",
        "attemptCount": 1,
        "startedAt": "2026-01-02T00:00:01Z",
        "finishedAt": "2026-01-02T00:00:04Z"
      }
    ]
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `404 RUN_NOT_FOUND`
* `403 AUTH_FORBIDDEN`

### Acceptance Criteria

* Returns all step runs for the run.
* Step runs are immutable and read-only.

### TDD Test Scenarios

* list steps success
* run not found
* tenant isolation

---

### 10.2.2 GET /api/v1/workflow-runs/{runId}/steps/{stepRunId}

### Summary

Get details for one step run.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

* `runId`
* `stepRunId`

### Database Operations

* Read from `step_runs`

### Success Response

```json
{
  "success": true,
  "data": {
    "stepRunId": "uuid",
    "workflowNodeId": "uuid",
    "nodeKey": "send_to_crm",
    "status": "succeeded",
    "attemptCount": 1,
    "inputPayload": {},
    "outputPayload": {},
    "errorPayload": null,
    "startedAt": "2026-01-02T00:00:01Z",
    "finishedAt": "2026-01-02T00:00:04Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `404 STEP_NOT_FOUND`
* `404 RUN_NOT_FOUND`
* `403 AUTH_FORBIDDEN`
* `401 AUTH_UNAUTHORIZED`

### Acceptance Criteria

* Returns step details for the specified run.
* Payloads are tenant-scoped and redacted where necessary.

### TDD Test Scenarios

* get step success
* step not found
* tenant isolation

---

## 10.3 Execution Log API

Execution logs are append-only records created by the worker.

### 10.3.1 GET /api/v1/workflow-runs/{runId}/logs

### Summary

List execution logs for a workflow run.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

* `runId`

### Query Parameters

* `page`
* `pageSize`
* `level` optional
* `stepRunId` optional

### Business Rules

* Logs are read-only.
* Sensitive values must be redacted.
* Tenant isolation must be enforced.

### Database Operations

* Read from `execution_logs`

### Events Published

* None

### Success Response

```json
{
  "success": true,
  "data": {
    "items": [
      {
        "logId": "uuid",
        "stepRunId": "uuid",
        "level": "info",
        "message": "Request sent successfully",
        "createdAt": "2026-01-02T00:00:03Z"
      }
    ],
    "pagination": {
      "page": 1,
      "pageSize": 50,
      "totalItems": 1,
      "totalPages": 1,
      "hasNext": false,
      "hasPrev": false
    }
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `404 RUN_NOT_FOUND`
* `403 AUTH_FORBIDDEN`

### Acceptance Criteria

* Returns logs for the run.
* Logs are paginated.
* Logs are redacted where necessary.

### TDD Test Scenarios

* list logs success
* filter by level
* filter by step
* tenant isolation

---

## 10.4 AI Analysis API

### 10.4.1 POST /api/v1/workflow-runs/{runId}/analysis

### Summary

Generate an AI-assisted diagnosis for a failed workflow run.

### Description

This endpoint sends redacted failure context to the LLM provider and returns a structured diagnosis that helps explain the failure and suggest a fix.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

* `runId`

### Headers

* `Content-Type: application/json`

### Request Body

```json
{
  "includeLogs": true,
  "logLimit": 20
}
```

### Validation Rules

* `logLimit` must be positive when provided.
* `includeLogs` defaults to `true` if omitted.

### Business Rules

* The run must belong to the authenticated tenant.
* The run should preferably be in failed state, though a diagnostic may also be generated for non-terminal runs if allowed by implementation.
* Secrets and sensitive payloads must be redacted before sending data to the model.
* The response must conform to a strict schema.
* Malformed model output must be rejected and retried within a defined limit.

### Side Effects

* May store analysis artifact.
* May write audit log for AI usage if desired.
* May consume tokens from a quota or usage meter in future phases.

### Database Operations

* Read from `workflow_runs`
* Read from `step_runs`
* Read from `execution_logs`
* Read from `workflows`
* Read from `workflow_versions`
* Optional insert into AI analysis table if one is later added
* Optional insert into `audit_logs`

### Events Published

* `workflow.run.analysisRequested`
* `workflow.run.analysisGenerated`

### Execution Flow

```text
HTTP Request
  -> Authenticate
  -> Authorize
  -> Load run and recent logs
  -> Redact secrets and sensitive payloads
  -> Build prompt
  -> Call LLM provider
  -> Validate AI response schema
  -> Retry on malformed output if needed
  -> Return diagnosis
```

### Expected Analysis Output Schema

```json
{
  "diagnosis": "Step failed due to downstream timeout",
  "possibleCause": "The HTTP step exceeded the configured timeout",
  "suggestedFix": "Increase timeout or reduce payload size",
  "confidence": 0.82
}
```

### Success Response

```json
{
  "success": true,
  "data": {
    "runId": "uuid",
    "diagnosis": "Step failed due to downstream timeout",
    "possibleCause": "The HTTP step exceeded the configured timeout",
    "suggestedFix": "Increase timeout or reduce payload size",
    "confidence": 0.82
  },
  "meta": null,
  "error": null
}
```

### Error Responses

* `401 AUTH_UNAUTHORIZED`
* `404 RUN_NOT_FOUND`
* `403 AUTH_FORBIDDEN`
* `409 AI_INVALID_RESPONSE`
* `503 AI_GENERATION_FAILED`

### Acceptance Criteria

* The endpoint returns a structured diagnosis.
* The output is schema-validated.
* Sensitive data is not exposed to the model.
* The result is tenant-scoped.

### TDD Test Scenarios

* successful analysis
* analysis for not found run
* malformed AI response
* LLM failure
* redaction behavior

---

## 10.5 Execution Lifecycle Rules

The following rules apply to all execution-related endpoints:

* Execution endpoints reflect lifecycle actions, not database CRUD.
* Trigger, retry, and cancel endpoints are command endpoints and usually return `202 Accepted`.
* Read endpoints are query endpoints and return `200 OK`.
* Step runs and execution logs are immutable from the public API.
* Retry does not overwrite historical runs.
* Cancel requests must be safe and idempotent.
* All execution-related data must remain tenant-scoped.

---

## 10.6 Shared Acceptance Criteria

The execution API is complete when:

* workflows can be triggered as runs,
* runs can be inspected and controlled through lifecycle actions,
* step runs and logs can be viewed read-only,
* failed runs can be analyzed using AI,
* and the API contract is detailed enough for TDD and OpenAPI generation.

---

## 10.7 Next Part

Part 4 will define:

* SSE contract
* security rules
* API versioning strategy
* API lifecycle states
* TDD contract strategy
* future evolution
* definition of done

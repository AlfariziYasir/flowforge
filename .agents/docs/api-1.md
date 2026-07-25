# FlowForge — API Specification v1

**Status:** Draft v1
**Scope:** Part 1 of 4 — Foundation
**Purpose:** Define the API contract for FlowForge in an API-first, TDD-friendly, OpenAPI-ready format.

---

## 1. Overview

This document defines the REST API contract for FlowForge, a multi-tenant workflow orchestration platform.

The API is the canonical contract between:

- the backend implementation,
- the frontend application,
- and the AI-assisted implementation workflow.

The API must support:

- authentication and authorization,
- tenant isolation,
- workflow CRUD and versioning,
- manual workflow execution,
- run history and logs,
- server-sent events for live monitoring,
- and one AI-assisted feature.

This document is intentionally designed to be convertible into OpenAPI 3.1 with minimal friction.

---

## 2. API Goals

The API must:

1. Provide a stable and consistent contract for all client applications.
2. Enforce tenant boundaries on every request.
3. Support workflow authoring, publishing, execution, and inspection.
4. Expose execution state in a form that is easy to consume by the frontend.
5. Be easy to implement with TDD.
6. Be safe for AI-assisted code generation.
7. Use predictable request and response shapes.
8. Avoid ambiguous behavior in status codes, errors, and pagination.

---

## 3. API Design Principles

### 3.1 RESTful first

The API uses resource-oriented REST endpoints.
Actions that modify state should map to clear resource operations whenever possible.

### 3.2 API-first

The API contract is defined before implementation.
Frontend and backend development should follow the same contract.

### 3.3 Stateless requests

Every request must contain enough information to be processed independently.
Server-side session state should not be required for core API behavior.

### 3.4 Tenant-aware by default

All protected endpoints operate within the scope of a single tenant.
Tenant identity is derived from the authenticated token, not from user-supplied body fields.

### 3.5 Idempotent where needed

State-changing endpoints that may be retried should support idempotency keys or equivalent deduplication mechanisms.

### 3.6 Consistent naming

Use plural resource names for collections and stable snake_case or lowerCamelCase consistently within payloads. For this project, JSON payloads should use `camelCase`.

### 3.7 OpenAPI-ready

Each endpoint should be describable with:

- method,
- path,
- headers,
- path parameters,
- query parameters,
- request body,
- response body,
- validation rules,
- error responses,
- and examples.

### 3.8 TDD-friendly

Each endpoint must be specifiable as acceptance criteria and test cases before implementation.

---

## 4. Authentication & Authorization

### 4.1 Authentication model

FlowForge uses JWT-based authentication.

The access token must include at least:

- `sub` — user ID,
- `tenantId` — tenant ID,
- `role` — user role,
- `iat` — issued at,
- `exp` — expiration.

### 4.2 Authorization model

The platform supports the following roles:

- `admin`
- `editor`
- `viewer`

### 4.3 Role capabilities

| Action                     | Admin | Editor | Viewer |
| -------------------------- | ----: | -----: | -----: |
| View workflows             |   Yes |    Yes |    Yes |
| Create workflow            |   Yes |    Yes |     No |
| Edit workflow              |   Yes |    Yes |     No |
| Publish workflow version   |   Yes |    Yes |     No |
| Roll back workflow version |   Yes |    Yes |     No |
| Trigger workflow run       |   Yes |    Yes |     No |
| View run history           |   Yes |    Yes |    Yes |
| View logs                  |   Yes |    Yes |    Yes |
| View AI analysis           |   Yes |    Yes |    Yes |
| Manage users               |   Yes |     No |     No |
| Change roles               |   Yes |     No |     No |

### 4.4 Tenant isolation

Every protected request must be authorized against the tenant in the token.
A user must never access data outside their tenant.
Tenant ID must not be accepted from client request bodies for protected resources.

### 4.5 Authentication headers

Authenticated requests must include:

```http
Authorization: Bearer <access_token>
```

### 4.6 Unauthenticated behavior

Unauthenticated requests to protected endpoints must return `401 Unauthorized`.
Unauthorized role access must return `403 Forbidden`.

---

## 5. API Conventions

### 5.1 Base path

All versioned endpoints use:

```text
/api/v1
```

### 5.2 Content type

All JSON endpoints use:

```http
Content-Type: application/json
```

### 5.3 Request body rules

- Request bodies must be validated.
- Unknown fields should be rejected unless explicitly allowed.
- Empty string validation should be enforced where required.

### 5.4 Response envelope

All standard JSON responses should use a consistent envelope.

Success response:

```json
{
  "success": true,
  "data": {},
  "meta": null,
  "error": null
}
```

Error response:

```json
{
  "success": false,
  "data": null,
  "meta": null,
  "error": {
    "code": "WORKFLOW_NOT_FOUND",
    "message": "Workflow not found",
    "details": null
  }
}
```

### 5.5 Pagination

List endpoints must support pagination.
Recommended query parameters:

- `page`
- `pageSize`

Pagination metadata should include:

- `page`
- `pageSize`
- `totalItems`
- `totalPages`
- `hasNext`
- `hasPrev`

### 5.6 Filtering

List endpoints may support filters relevant to the resource, such as:

- status,
- workflowId,
- createdAtFrom,
- createdAtTo,
- triggerType.

### 5.7 Sorting

Sorting should be explicit if supported.
Default sort should be stable and documented.

### 5.8 Idempotency key

State-changing endpoints that create runs or external side effects should support:

```http
Idempotency-Key: <opaque_key>
```

### 5.9 Correlation ID

Requests may include an optional correlation header:

```http
X-Request-Id: <request_id>
```

If absent, the server may generate one.

### 5.10 Error handling rules

- Use HTTP status codes appropriately.
- Return domain error codes in the error envelope.
- Never leak stack traces or sensitive internal details to clients.

---

## 6. Standard Request / Response Patterns

### 6.1 Standard list response

```json
{
  "success": true,
  "data": {
    "items": [],
    "pagination": {
      "page": 1,
      "pageSize": 20,
      "totalItems": 0,
      "totalPages": 0,
      "hasNext": false,
      "hasPrev": false
    }
  },
  "meta": null,
  "error": null
}
```

### 6.2 Standard single-resource response

```json
{
  "success": true,
  "data": {
    "id": "..."
  },
  "meta": null,
  "error": null
}
```

### 6.3 Standard empty response

For delete or other no-content operations, use either:

- `204 No Content`, or
- a success envelope with an empty payload,

but use one pattern consistently across the project.

Recommended MVP approach: `204 No Content` for delete operations.

---

## 7. Error Catalog

### 7.1 Error format

All application errors should use a stable domain code.

```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable message",
    "details": {}
  }
}
```

### 7.2 Common error codes

#### Authentication and authorization

- `AUTH_UNAUTHORIZED`
- `AUTH_FORBIDDEN`
- `AUTH_INVALID_TOKEN`
- `AUTH_TOKEN_EXPIRED`

#### Tenant and access

- `TENANT_NOT_FOUND`
- `TENANT_ACCESS_DENIED`

#### Workflow

- `WORKFLOW_NOT_FOUND`
- `WORKFLOW_ALREADY_EXISTS`
- `WORKFLOW_NAME_REQUIRED`
- `WORKFLOW_VERSION_NOT_FOUND`
- `WORKFLOW_VERSION_CONFLICT`
- `WORKFLOW_VERSION_IMMUTABLE`
- `WORKFLOW_INVALID_DAG`
- `WORKFLOW_CYCLE_DETECTED`
- `WORKFLOW_NODE_INVALID`
- `WORKFLOW_EDGE_INVALID`

#### Execution

- `RUN_NOT_FOUND`
- `RUN_ALREADY_RUNNING`
- `RUN_ALREADY_COMPLETED`
- `RUN_TIMEOUT`
- `RUN_CLAIM_FAILED`
- `STEP_NOT_FOUND`
- `STEP_CLAIM_FAILED`
- `STEP_TIMEOUT`
- `STEP_EXECUTION_FAILED`

#### AI

- `AI_INVALID_RESPONSE`
- `AI_RESPONSE_SCHEMA_MISMATCH`
- `AI_GENERATION_FAILED`

#### Validation

- `VALIDATION_ERROR`
- `INVALID_REQUEST_BODY`
- `INVALID_QUERY_PARAMETER`
- `INVALID_PATH_PARAMETER`

#### System

- `RATE_LIMITED`
- `INTERNAL_SERVER_ERROR`
- `SERVICE_UNAVAILABLE`

### 7.3 HTTP mapping

Suggested mappings:

- `400` — validation or malformed input
- `401` — unauthenticated
- `403` — forbidden
- `404` — resource not found
- `409` — version conflict, idempotency conflict, or duplicate state transition
- `422` — semantic validation failure if needed
- `429` — rate limited
- `500` — unexpected internal error
- `503` — dependent service unavailable

---

## 8. Resource Model

### 8.1 Auth resource

Used for login, token refresh, and logout.

### 8.2 User resource

Represents tenant-scoped users and role membership.

### 8.3 Workflow resource

Represents the workflow metadata and current version pointer.

### 8.4 Workflow version resource

Represents an immutable snapshot of a workflow definition.

### 8.5 Workflow run resource

Represents one execution instance of a workflow version.

### 8.6 Step run resource

Represents one node execution inside a workflow run.

### 8.7 Execution log resource

Represents runtime logs for a workflow run or step run.

### 8.8 Audit log resource

Represents business actions and administrative changes.

### 8.9 AI resource

Represents AI-assisted analysis or workflow generation output.

---

## 9. Global Validation Rules

The following rules apply throughout the API:

- IDs must be valid UUIDs.
- Names must be non-empty where required.
- Tenant-scoped resources must always be resolved through tenant-aware repositories.
- Workflow definitions must be validated before publish or execution.
- Retry and timeout values must be positive when provided.
- Inputs to HTTP steps must be sanitized before execution.
- AI-generated content must be validated against schema before being accepted.

---

## 10. Endpoint Specification Template

Every endpoint in Part 2 and Part 3 must include:

- summary
- description
- method
- path
- authentication
- authorization
- path parameters
- query parameters
- request body schema
- response body schema
- success status code
- error status codes
- validation rules
- example request
- example response
- acceptance criteria

---

## 11. Definition of Done for the API Foundation

The API foundation is complete when:

- authentication and authorization rules are documented,
- response and error envelopes are standardized,
- pagination and filtering conventions are defined,
- tenant isolation is explicit,
- and the document is ready to be extended with resource-level endpoint definitions.

---

## 12. Next Part

Part 2 will define the core resource endpoints:

- auth
- users
- workflows
- workflow versions

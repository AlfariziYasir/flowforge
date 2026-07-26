# FlowForge — API Specification v1

**Status:** Draft v1
**Scope:** Part 2 of 4 — Core Management API
**Purpose:** Define the core REST API contract for authentication, user management, workflow management, and workflow version management.

> This document follows the contract style established in Part 1.
> All responses use the standard envelope unless otherwise noted.

---

## 9. Core Resource Endpoints

---

## 9.1 Authentication API

### 9.1.1 POST /api/v1/auth/login

### Summary

Authenticate a user and issue access and refresh tokens.

### Description

This endpoint validates user credentials and returns JWT tokens scoped to the user tenant and role.

### Authentication

Not required.

### Authorization

Not required.

### Headers

- `Content-Type: application/json`

### Request Body

```json
{
  "email": "user@company.com",
  "password": "secret123"
}
```

### Validation Rules

- `email` is required and must be a valid email format.
- `password` is required and must not be empty.

### Business Rules

- The user must exist.
- The user must be active.
- The password must match the stored hash.
- The tenant context is derived from the authenticated user record.

### Side Effects

- May record a login audit event.
- May rotate refresh token state if token rotation is enabled.

### Database Operations

- Read from `users`
- Read from `tenants`
- Insert into `audit_logs` (optional)

### Events Published

- `auth.login.succeeded` (optional)
- `auth.login.failed` (optional)

### Success Response

```json
{
  "success": true,
  "data": {
    "accessToken": "jwt-access-token",
    "refreshToken": "jwt-refresh-token",
    "tokenType": "Bearer",
    "expiresIn": 3600,
    "user": {
      "id": "uuid",
      "tenantId": "uuid",
      "email": "user@company.com",
      "role": "editor"
    }
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `401 AUTH_INVALID_TOKEN` — invalid credentials
- `403 AUTH_FORBIDDEN` — user is inactive
- `422 VALIDATION_ERROR` — malformed request body

### Acceptance Criteria

- Valid credentials return tokens.
- Invalid credentials return `401`.
- Inactive users cannot log in.

### TDD Test Scenarios

- successful login
- invalid password
- invalid email format
- inactive user

### Example Request

```json
{
  "email": "user@company.com",
  "password": "secret123"
}
```

### Example Response

```json
{
  "success": true,
  "data": {
    "accessToken": "jwt-access-token",
    "refreshToken": "jwt-refresh-token",
    "tokenType": "Bearer",
    "expiresIn": 3600,
    "user": {
      "id": "b4b4f5d2-7f4e-4c0a-9cf3-3f8f8a2f1b11",
      "tenantId": "1d1c8e4d-0a14-4f51-b8b0-2e5d5b74d2ff",
      "email": "user@company.com",
      "role": "editor"
    }
  },
  "meta": null,
  "error": null
}
```

---

### 9.1.2 POST /api/v1/auth/refresh

### Summary

Rotate or refresh the access token using a valid refresh token.

### Authentication

Not required. Refresh token is used instead.

### Headers

- `Content-Type: application/json`

### Request Body

```json
{
  "refreshToken": "jwt-refresh-token"
}
```

### Validation Rules

- `refreshToken` is required.

### Business Rules

- Refresh token must be valid and not expired.
- The token must belong to the authenticated tenant and user.
- Token rotation may invalidate the old refresh token.

### Side Effects

- May invalidate the old refresh token.
- May issue a new refresh token.

### Database Operations

- Read from token/session storage if enabled
- Read from `users`

### Events Published

- `auth.token.refreshed` (optional)

### Success Response

```json
{
  "success": true,
  "data": {
    "accessToken": "new-access-token",
    "refreshToken": "new-refresh-token",
    "tokenType": "Bearer",
    "expiresIn": 3600
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `401 AUTH_INVALID_TOKEN`
- `401 AUTH_TOKEN_EXPIRED`
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Valid refresh token returns a new access token.
- Expired refresh token returns `401`.

### TDD Test Scenarios

- valid refresh
- expired refresh
- malformed refresh token

---

### 9.1.3 POST /api/v1/auth/logout

### Summary

Invalidate the current authentication session or refresh token.

### Authentication

Required.

### Authorization

Any authenticated user.

### Headers

- `Authorization: Bearer <access_token>`
- `Content-Type: application/json`

### Request Body

```json
{
  "refreshToken": "jwt-refresh-token"
}
```

### Validation Rules

- `refreshToken` is optional if logout is access-token-only.
- If provided, it must be valid.

### Business Rules

- Logout should invalidate refresh token state when supported.
- Access token may remain valid until expiry if stateless JWT is used.

### Side Effects

- May revoke refresh token.
- May write audit log.

### Database Operations

- Update token/session storage if enabled
- Insert into `audit_logs`

### Events Published

- `auth.logout.succeeded` (optional)

### Success Response

```json
{
  "success": true,
  "data": {
    "loggedOut": true
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `401 AUTH_UNAUTHORIZED`
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Authenticated users can logout.
- Revoked refresh tokens can no longer be used.

### TDD Test Scenarios

- logout success
- logout without refresh token
- unauthorized logout

---

## 9.2 Current User API

### 9.2.1 GET /api/v1/users/me

### Summary

Return the currently authenticated user profile.

### Authentication

Required.

### Authorization

Any authenticated role.

### Headers

- `Authorization: Bearer <access_token>`

### Business Rules

- The response must reflect the token identity.
- The tenant must come from the token.

### Database Operations

- Read from `users`
- Read from `tenants`

### Events Published

- None

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "tenantId": "uuid",
    "email": "user@company.com",
    "role": "editor",
    "tenant": {
      "id": "uuid",
      "slug": "acme",
      "name": "Acme"
    }
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `401 AUTH_UNAUTHORIZED`
- `404 TENANT_NOT_FOUND`

### Acceptance Criteria

- Returns the current user identity and tenant info.
- Works for all authenticated roles.

### TDD Test Scenarios

- get current user
- unauthorized request

---

## 9.3 User Management API

### 9.3.1 GET /api/v1/users

### Summary

List users in the current tenant.

### Authentication

Required.

### Authorization

`admin`, `editor` may read user list.

### Query Parameters

- `page`
- `pageSize`
- `role` (optional)
- `isActive` (optional)

### Business Rules

- Users are tenant-scoped.
- Viewer can read only if policy allows; recommended default: no user list for viewer.
- Results must not leak users from other tenants.

### Database Operations

- Read from `users`

### Success Response

```json
{
  "success": true,
  "data": {
    "items": [
      {
        "id": "uuid",
        "email": "user@company.com",
        "role": "editor",
        "isActive": true,
        "createdAt": "2026-01-01T00:00:00Z"
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

- `401 AUTH_UNAUTHORIZED`
- `403 AUTH_FORBIDDEN`

### Acceptance Criteria

- Tenant-scoped list is returned.
- Pagination works.

### TDD Test Scenarios

- list users success
- forbidden for viewer
- pagination

---

### 9.3.2 POST /api/v1/users

### Summary

Create a tenant user.

### Authentication

Required.

### Authorization

`admin` only.

### Headers

- `Content-Type: application/json`

### Request Body

```json
{
  "email": "new.user@company.com",
  "password": "secret123",
  "role": "viewer"
}
```

### Validation Rules

- email required and valid
- password required and strong enough according to policy
- role must be one of `admin`, `editor`, `viewer`

### Business Rules

- Email must be unique within the tenant.
- Newly created user must belong to the authenticated tenant.

### Side Effects

- May write audit log.
- May send invitation in a future phase, but not required in MVP.

### Database Operations

- Insert into `users`
- Insert into `audit_logs`

### Events Published

- `user.created`

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "tenantId": "uuid",
    "email": "new.user@company.com",
    "role": "viewer",
    "isActive": true
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `403 AUTH_FORBIDDEN`
- `409 WORKFLOW_ALREADY_EXISTS` is not relevant here; use `409 VALIDATION_ERROR` or `USER_ALREADY_EXISTS` if added to the catalog
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Admin can create tenant user.
- Duplicate email in the same tenant is rejected.

### TDD Test Scenarios

- create user success
- duplicate email
- forbidden for non-admin

---

### 9.3.3 PATCH /api/v1/users/{userId}

### Summary

Update a tenant user.

### Authentication

Required.

### Authorization

`admin` only.

### Path Parameters

- `userId`

### Request Body

```json
{
  "role": "editor",
  "isActive": true
}
```

### Validation Rules

- `userId` must be a UUID.
- `role` must be valid if provided.
- `isActive` must be boolean if provided.

### Business Rules

- The target user must belong to the same tenant.
- Admins may not accidentally remove their own last admin access if a tenant safety rule is enforced.

### Side Effects

- May write audit log.

### Database Operations

- Update `users`
- Insert into `audit_logs`

### Events Published

- `user.updated`

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "email": "new.user@company.com",
    "role": "editor",
    "isActive": true
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `403 AUTH_FORBIDDEN`
- `404 USER_NOT_FOUND`
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Admin can update tenant user.
- Cross-tenant update is rejected.

### TDD Test Scenarios

- update role
- deactivate user
- not found
- forbidden

---

### 9.3.4 DELETE /api/v1/users/{userId}

### Summary

Deactivate or soft-delete a tenant user.

### Authentication

Required.

### Authorization

`admin` only.

### Path Parameters

- `userId`

### Business Rules

- Prefer soft delete or deactivation instead of hard delete.
- The action must remain tenant-scoped.
- The current authenticated user should not be able to remove the last remaining admin if safety rule is enabled.

### Side Effects

- May mark user inactive.
- May write audit log.

### Database Operations

- Update `users.isActive`
- Insert into `audit_logs`

### Events Published

- `user.deleted` or `user.deactivated`

### Success Response

- `204 No Content`

### Error Responses

- `403 AUTH_FORBIDDEN`
- `404 USER_NOT_FOUND`

### Acceptance Criteria

- User is deactivated for the tenant.
- The user cannot authenticate after deactivation.

### TDD Test Scenarios

- delete user success
- forbidden
- not found

---

## 9.4 Workflow Management API

### 9.4.1 GET /api/v1/workflows

### Summary

List workflows for the current tenant.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Query Parameters

- `page`
- `pageSize`
- `status`
- `search`
- `sortBy`
- `sortOrder`

### Business Rules

- Results must be tenant-scoped.
- Viewers can read workflows.
- Deleted or archived workflows may be excluded by default unless explicitly requested.

### Database Operations

- Read from `workflows`

### Events Published

- None

### Success Response

```json
{
  "success": true,
  "data": {
    "items": [
      {
        "id": "uuid",
        "name": "Lead Sync",
        "description": "Sync leads to CRM",
        "status": "draft",
        "currentVersionNumber": 1,
        "updatedAt": "2026-01-01T00:00:00Z"
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

- `401 AUTH_UNAUTHORIZED`
- `403 AUTH_FORBIDDEN`

### Acceptance Criteria

- Workflows are listed only for current tenant.
- Pagination and search work.

### TDD Test Scenarios

- list workflows success
- empty list
- tenant isolation

---

### 9.4.2 GET /api/v1/workflows/{workflowId}

### Summary

Get a single workflow with current version summary.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

- `workflowId`

### Database Operations

- Read from `workflows`
- Read from `workflow_versions`

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "name": "Lead Sync",
    "description": "Sync leads to CRM",
    "status": "draft",
    "currentVersionNumber": 1,
    "currentVersionId": "uuid",
    "rowVersion": 3,
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-02T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `404 WORKFLOW_NOT_FOUND`
- `403 AUTH_FORBIDDEN`

### Acceptance Criteria

- Returns workflow metadata for tenant.
- Current version summary is included.

### TDD Test Scenarios

- get workflow success
- not found
- tenant isolation

---

### 9.4.3 POST /api/v1/workflows

### Summary

Create a new workflow draft.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Headers

- `Content-Type: application/json`
- `Idempotency-Key: <opaque_key>` (recommended)

### Request Body

```json
{
  "name": "Lead Sync",
  "description": "Sync leads to CRM"
}
```

### Validation Rules

- `name` is required.
- `name` must be unique within tenant.
- `description` is optional.

### Business Rules

- A created workflow starts as `draft`.
- A workflow belongs to the authenticated tenant.
- No graph is required at creation time.

### Side Effects

- Write audit log.

### Database Operations

- Insert into `workflows`
- Insert into `audit_logs`
- Potentially insert idempotency key record

### Events Published

- `workflow.created`

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "tenantId": "uuid",
    "name": "Lead Sync",
    "description": "Sync leads to CRM",
    "status": "draft",
    "currentVersionNumber": 0,
    "rowVersion": 1,
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-01T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `403 AUTH_FORBIDDEN`
- `409 WORKFLOW_ALREADY_EXISTS`
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Workflow draft is created for the current tenant.
- Duplicate names in the same tenant are rejected.

### TDD Test Scenarios

- create workflow success
- duplicate name
- forbidden for viewer
- validation error

---

### 9.4.4 PATCH /api/v1/workflows/{workflowId}

### Summary

Update workflow metadata draft.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Path Parameters

- `workflowId`

### Request Body

```json
{
  "name": "Lead Sync Updated",
  "description": "Updated description",
  "rowVersion": 3
}
```

### Validation Rules

- `rowVersion` is required for optimistic locking.
- `name` must remain unique within tenant if changed.

### Business Rules

- Only draft or editable workflows can be updated.
- Published immutable versions must not be modified.
- Update must fail if row version is stale.

### Side Effects

- Write audit log.

### Database Operations

- Update `workflows` using optimistic locking
- Insert into `audit_logs`

### Events Published

- `workflow.updated`

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "name": "Lead Sync Updated",
    "description": "Updated description",
    "rowVersion": 4,
    "updatedAt": "2026-01-02T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `404 WORKFLOW_NOT_FOUND`
- `409 WORKFLOW_VERSION_CONFLICT`
- `403 AUTH_FORBIDDEN`
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Draft can be updated with correct row version.
- Stale updates are rejected.

### TDD Test Scenarios

- update workflow success
- stale row version conflict
- not found
- forbidden

---

### 9.4.5 DELETE /api/v1/workflows/{workflowId}

### Summary

Archive or soft-delete a workflow.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Path Parameters

- `workflowId`

### Business Rules

- Prefer soft delete or archive rather than hard delete.
- Published workflow may need to be archived, not physically removed.
- Historical runs and versions must remain accessible.

### Side Effects

- Write audit log.
- May mark workflow as `archived`.

### Database Operations

- Update `workflows.status`
- Insert into `audit_logs`

### Events Published

- `workflow.deleted` or `workflow.archived`

### Success Response

- `204 No Content`

### Error Responses

- `404 WORKFLOW_NOT_FOUND`
- `403 AUTH_FORBIDDEN`
- `409 WORKFLOW_VERSION_IMMUTABLE` if delete is blocked by state

### Acceptance Criteria

- Workflow is no longer active after deletion or archiving.
- Historical data remains intact.

### TDD Test Scenarios

- delete workflow success
- archived workflow state
- not found
- forbidden

---

## 9.5 Workflow Version API

### 9.5.1 GET /api/v1/workflows/{workflowId}/versions

### Summary

List all versions for a workflow.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

- `workflowId`

### Database Operations

- Read from `workflow_versions`

### Success Response

```json
{
  "success": true,
  "data": {
    "items": [
      {
        "id": "uuid",
        "workflowId": "uuid",
        "versionNumber": 1,
        "status": "published",
        "publishedAt": "2026-01-01T00:00:00Z",
        "createdAt": "2026-01-01T00:00:00Z"
      }
    ]
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `404 WORKFLOW_NOT_FOUND`

### Acceptance Criteria

- Versions are returned in tenant scope.
- Latest version can be identified by version number.

### TDD Test Scenarios

- list versions success
- no versions
- tenant isolation

---

### 9.5.2 GET /api/v1/workflows/{workflowId}/versions/{versionId}

### Summary

Get one workflow version, including its graph snapshot.

### Authentication

Required.

### Authorization

`admin`, `editor`, `viewer`.

### Path Parameters

- `workflowId`
- `versionId`

### Database Operations

- Read from `workflow_versions`
- Read from `workflow_nodes`
- Read from `workflow_edges`

### Success Response

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "workflowId": "uuid",
    "versionNumber": 1,
    "status": "published",
    "graphSnapshot": {
      "nodes": [],
      "edges": []
    },
    "metadata": {},
    "checksum": "sha256-hash",
    "createdAt": "2026-01-01T00:00:00Z",
    "publishedAt": "2026-01-01T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `404 WORKFLOW_VERSION_NOT_FOUND`
- `403 AUTH_FORBIDDEN`

### Acceptance Criteria

- Version content is returned and tenant-scoped.
- Graph snapshot is available for rendering and audit.

### TDD Test Scenarios

- get version success
- version not found

---

### 9.5.3 POST /api/v1/workflows/{workflowId}/versions/publish

### Summary

Publish the current workflow draft as an immutable version.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Headers

- `Content-Type: application/json`
- `Idempotency-Key: <opaque_key>` recommended

### Request Body

```json
{
  "rowVersion": 4
}
```

### Validation Rules

- `rowVersion` is required.
- Current draft must be valid before publish.

### Business Rules

- Publish creates a new immutable workflow version.
- Workflow version number increments monotonically.
- Graph must be a valid DAG.
- Published versions must not be modified.

### Side Effects

- Create version snapshot.
- Write audit log.
- Possibly mark workflow status as `published`.

### Database Operations

- Insert into `workflow_versions`
- Insert into `workflow_nodes`
- Insert into `workflow_edges`
- Update `workflows.current_version_id` and `current_version_number`
- Update `workflows.row_version`
- Insert into `audit_logs`

### Events Published

- `workflow.published`

### Success Response

```json
{
  "success": true,
  "data": {
    "workflowId": "uuid",
    "versionId": "uuid",
    "versionNumber": 2,
    "status": "published",
    "publishedAt": "2026-01-02T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `404 WORKFLOW_NOT_FOUND`
- `409 WORKFLOW_VERSION_CONFLICT`
- `409 WORKFLOW_INVALID_DAG`
- `409 WORKFLOW_CYCLE_DETECTED`
- `422 VALIDATION_ERROR`

### Acceptance Criteria

- Publishing a valid draft creates a new immutable version.
- Stale row version publish is rejected.
- Invalid DAG cannot be published.

### TDD Test Scenarios

- publish success
- stale row version
- invalid DAG
- cycle detected

---

### 9.5.4 POST /api/v1/workflows/{workflowId}/versions/{versionId}/rollback

### Summary

Roll back the workflow to a previous published version.

### Authentication

Required.

### Authorization

`admin`, `editor`.

### Path Parameters

- `workflowId`
- `versionId`

### Headers

- `Content-Type: application/json`

### Request Body

```json
{
  "rowVersion": 4
}
```

### Business Rules

- Only published or archived historical versions can be rolled back.
- Rollback should create a new active current version pointer.
- Rollback should not mutate the historical version snapshot.
- If implementation chooses, rollback may create a new version cloned from the target version rather than reusing the old row.

### Side Effects

- Update current version pointer.
- Write audit log.
- May create a new workflow version snapshot cloned from the selected historical version.

### Database Operations

- Read target `workflow_versions`
- Update `workflows.current_version_id` and `current_version_number`
- Update `workflows.row_version`
- Insert into `audit_logs`
- Optional insert into `workflow_versions` if rollback clones a new version

### Events Published

- `workflow.rolledBack`

### Success Response

```json
{
  "success": true,
  "data": {
    "workflowId": "uuid",
    "versionId": "uuid",
    "versionNumber": 1,
    "status": "published",
    "rolledBackAt": "2026-01-02T00:00:00Z"
  },
  "meta": null,
  "error": null
}
```

### Error Responses

- `404 WORKFLOW_NOT_FOUND`
- `404 WORKFLOW_VERSION_NOT_FOUND`
- `409 WORKFLOW_VERSION_CONFLICT`
- `403 AUTH_FORBIDDEN`

### Acceptance Criteria

- A historical version can be restored as the active version.
- Rollback is tenant-scoped and safe under concurrency.

### TDD Test Scenarios

- rollback success
- version not found
- stale row version
- forbidden for viewer

---

## 9.6 Shared Workflow Rules

The following rules apply to all workflow and workflow version endpoints:

- Tenant isolation must always be enforced.
- Workflow versions are immutable after publish.
- Workflow metadata updates must use optimistic locking.
- Publish and rollback operations must be atomic.
- Audit logs must be written for user-visible workflow changes.

---

## 9.7 Part 2 Completion Criteria

Part 2 is complete when:

- authentication endpoints are defined,
- user management endpoints are defined,
- workflow CRUD endpoints are defined,
- workflow versioning endpoints are defined,
- all endpoints include business rules, database operations, events, acceptance criteria, and TDD scenarios.

---

## 9.8 Next Part

Part 3 will define the execution APIs:

- workflow runs
- step runs
- execution logs
- AI endpoints

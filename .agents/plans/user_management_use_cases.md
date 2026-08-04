# Implementation Plan — User Management Use Cases & Endpoints

This implementation plan outlines the architecture, transactional audit logging, RBAC enforcement, self-deactivation guardrails, and **Test-Driven Development (TDD) roadmap** for **User Management Use Cases** (`POST /api/v1/users`, `GET /api/v1/users`, `GET /api/v1/users/{userId}`, `PATCH /api/v1/users/{userId}`, `DELETE /api/v1/users/{userId}`) as specified in `api-2.md` Section 9.3 and `backlog.md`.

---

## User Review Required

> [!IMPORTANT]
> - **Location**: User management services and HTTP handlers will expand the existing `internal/auth` package.
> - **Transactional Audit Logging**: All user mutations (`POST`, `PATCH`, `DELETE`) execute inside a `UnitOfWork` transaction (`txManager.ExecuteInTx`), ensuring `users` table updates and `audit_logs` records are written atomically.
> - **Self-Deactivation Prevention**: The `DELETE /api/v1/users/{userId}` endpoint explicitly checks if `AuthUser.ID == targetUserID` and returns `400 Bad Request` (`CANNOT_DEACTIVATE_SELF`) to prevent admins from locking themselves out of their tenant.
> - **RBAC Authorization**:
>   - `POST`, `PATCH`, `DELETE`: Restricted to `admin` role (`RequireRole("admin")`).
>   - `GET /api/v1/users`, `GET /api/v1/users/{userId}`: Restricted to `admin` and `editor` roles (`RequireRole("admin", "editor")`). Viewers receive `403 Forbidden`.

---

## Open Questions

- None. All architectural choices (package location, transactional audit logging strategy, RBAC rules, self-deactivation guardrails) were resolved during the `/grill-me` design interview session.

---

## TDD Vertical Slices Roadmap

```mermaid
flowchart TD
    subgraph Slice 1: UserRepository Pagination
        A1[RED: Write ListUsers test in repository_test.go] --> A2[Verify Failure]
        A2 --> A3[GREEN: Implement ListUsers in repository.go] --> A4[Verify Pass]
    end
    subgraph Slice 2: Transactional Audit Logging
        B1[RED: Write audit logging tx tests] --> B2[Verify Failure]
        B2 --> B3[GREEN: Wire UnitOfWork in User mutations] --> B4[Verify Pass]
    end
    subgraph Slice 3: Create User Endpoint
        C1[RED: Write POST /users handler tests] --> C2[Verify Failure]
        C2 --> C3[GREEN: Implement CreateUser handler] --> C4[Verify Pass]
    end
    subgraph Slice 4: List & Get User Endpoints
        D1[RED: Write GET /users list & by ID tests] --> D2[Verify Failure]
        D2 --> D3[GREEN: Implement ListUsers & GetUser handlers] --> D4[Verify Pass]
    end
    subgraph Slice 5: Update & Delete User Endpoints
        E1[RED: Write PATCH & DELETE /users tests + self-deactivation guard] --> E2[Verify Failure]
        E2 --> E3[GREEN: Implement Update & Delete handlers] --> E4[Verify Pass]
    end
    subgraph Slice 6: Router Wiring & Verification
        F1[MODIFY: Register routes in cmd/api/main.go] --> F2[Run go test -v -race ./...] --> F3[Verify 0 Data Races]
    end

    Slice 1 --> Slice 2 --> Slice 3 --> Slice 4 --> Slice 5 --> Slice 6
```

---

## Detailed Proposed Changes & Seam Specifications

### Persistence Layer

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go)
- Add `ListUsers(ctx context.Context, tenantID uuid.UUID, page, pageSize int, roleFilter string, activeOnly *bool) ([]domain.User, int64, error)` method to `UserRepository` interface and `postgresUserRepository` struct.
- Uses Squirrel builder for tenant-isolated SQL query with pagination (`LIMIT $1 OFFSET $2`) and count query (`COUNT(*)`).

---

### Handler Layer & Business Logic

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
- Add user management request DTOs:
  - `CreateUserRequest` (`Email`, `Password`, `Role`)
  - `UpdateUserRequest` (`Role`, `IsActive`)
  - `UserResponse` envelope DTO (`json:"-"` on password hash)
- Implement HTTP Handler Methods:
  - `CreateUser(w, r)`: Validates body, hashes password via `PasswordService`, executes `txManager.ExecuteInTx` to insert user into `users` and audit log into `audit_logs` (`action = "user.created"`).
  - `ListUsers(w, r)`: Parses query params (`page`, `pageSize`, `role`, `isActive`), retrieves tenant users from `UserRepository`, returns paginated response envelope.
  - `GetUser(w, r)`: Retrieves user by ID within tenant context.
  - `UpdateUser(w, r)`: Validates role/active status changes, updates user via `txManager.ExecuteInTx` with audit log (`action = "user.updated"`).
  - `DeleteUser(w, r)`: Checks for self-deactivation (`AuthUser.ID == targetID`), deactivates user (`is_active = false`), inserts audit log (`action = "user.deactivated"`).

---

### Router Wiring & Entrypoint

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- Register User Management routes under `/api/v1/users`:
  - `POST /api/v1/users` → `authMiddleware.Authenticate`, `RequireRole("admin")`, `authHandler.CreateUser`
  - `GET /api/v1/users` → `authMiddleware.Authenticate`, `RequireRole("admin", "editor")`, `authHandler.ListUsers`
  - `GET /api/v1/users/{userId}` → `authMiddleware.Authenticate`, `RequireRole("admin", "editor")`, `authHandler.GetUser`
  - `PATCH /api/v1/users/{userId}` → `authMiddleware.Authenticate`, `RequireRole("admin")`, `authHandler.UpdateUser`
  - `DELETE /api/v1/users/{userId}` → `authMiddleware.Authenticate`, `RequireRole("admin")`, `authHandler.DeleteUser`

---

## Verification Plan

### Automated Tests
- `internal/auth/repository_test.go`: Test `ListUsers` pagination, role filters, and tenant isolation.
- `internal/auth/handler_test.go`:
  - `TestCreateUser_AdminSuccess`: Admin creates user, returns 201 Created and user DTO.
  - `TestCreateUser_DuplicateEmail`: Returns 409 Conflict.
  - `TestCreateUser_NonAdmin`: Returns 403 Forbidden.
  - `TestListUsers_Pagination`: Returns page 1 of 20 with `pagination` metadata.
  - `TestListUsers_ViewerForbidden`: Viewer role receives 403 Forbidden.
  - `TestUpdateUser_RoleChange`: Admin updates user role from `viewer` to `editor`.
  - `TestDeleteUser_DeactivateSuccess`: Admin deactivates target user.
  - `TestDeleteUser_SelfDeactivationBlocked`: Admin attempting to delete own user ID receives 400 Bad Request (`CANNOT_DEACTIVATE_SELF`).
- Full project test suite execution: `go test -v -race ./...`.

### Manual Verification
- Run local stack via `docker compose up`.
- Login as admin (`POST /api/v1/auth/login`) to obtain Bearer token.
- Execute `POST /api/v1/users` to create a new `editor` user.
- Execute `GET /api/v1/users` to verify the new user appears in the tenant user list.
- Attempt `DELETE /api/v1/users/{adminUserId}` to verify self-deactivation is blocked.

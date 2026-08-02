# Implementation Plan — Comprehensive Clean Architecture Across All Domains

This implementation plan establishes a comprehensive Clean Architecture refactoring and feature implementation across **all domain packages** (`internal/auth`, `internal/tenant`, `internal/user`, and future workflow modules) as mandated by Project Rule 4 in `.agents/AGENTS.md`.

---

## User Review Required

> [!IMPORTANT]
> - **Global Layer Standard**: Every domain feature package in `internal/` will enforce a strict 4-Tier Clean Architecture model:
>   - **Domain Layer (`domain/`)**: Pure domain models (`User`, `Tenant`, `Workflow`) and sentinel errors.
>   - **Repository Layer (`repository.go`)**: Database persistence adapters (`UserRepository`, `TenantRepository`, `BaseRepository[T]`).
>   - **UseCase / Application Layer (`usecase.go`)**: Pure business logic orchestrations (`AuthUseCase`, `TenantUseCase`, `UserUseCase`) with **zero `net/http` dependencies**.
>   - **Delivery / Transport Layer (`handler.go`)**: HTTP Controllers mapping JSON payloads to UseCases and translating domain errors to HTTP status codes.
> - **Domain Scope**:
>   - **Auth Domain (`internal/auth`)**: Decouple `AuthUseCase` (`Login`, `Refresh`, `Logout`, `GetMe`) from `AuthHandler`.
>   - **Tenant Domain (`internal/tenant`)**: Create `TenantUseCase` (`GetBySlug`, `GetByID`) and `TenantHandler`.
>   - **User Domain (`internal/auth` or `internal/user`)**: Create `UserUseCase` (`CreateUser`, `ListUsers`, `GetUser`, `UpdateUser`, `DeleteUser`) with transactional audit logging (`UnitOfWork`) and self-deactivation guardrails, decoupled from `UserHandler`.

---

## Clean Architecture Domain Layer Matrix

```mermaid
flowchart TD
    subgraph Delivery / Transport Layer (HTTP)
        H1[AuthHandler]
        H2[UserHandler]
        H3[TenantHandler]
    end

    subgraph Application / UseCase Layer (Pure Business Logic)
        U1[AuthUseCase]
        U2[UserUseCase]
        U3[TenantUseCase]
    end

    subgraph Infrastructure / Repository Layer (Persistence)
        R1[UserRepository]
        R2[TenantRepository]
        R3[TokenBlacklist]
        R4[UnitOfWork TxManager]
    end

    subgraph Core Domain Layer
        D1[User Entity]
        D2[Tenant Entity]
        D3[Sentinel Errors]
    end

    H1 --> U1
    H2 --> U2
    H3 --> U3

    U1 --> R1
    U1 --> R2
    U1 --> R3

    U2 --> R1
    U2 --> R4

    U3 --> R2

    R1 --> D1
    R2 --> D2
```

---

## Proposed Changes Across All Domains

### 1. Tenant Domain (`internal/tenant`)

#### [NEW] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/tenant/usecase.go)
- `TenantUseCase` interface: `GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error)`, `GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)`.
- `tenantUseCase` struct wrapping `TenantRepository`. Sanitizes slug inputs.

#### [NEW] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/tenant/usecase_test.go)
- Unit tests for `TenantUseCase`.

---

### 2. Auth Domain (`internal/auth`)

#### [NEW] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)
- `AuthResult` struct (`AccessToken`, `RefreshToken`, `ExpiresIn`, `User *domain.User`).
- `AuthUseCase` interface: `Login`, `Refresh`, `Logout`, `GetMe`.
- `authUseCase` struct orchestrating `TenantUseCase`, `UserRepository`, `JWTService`, `PasswordService`, `TokenBlacklist`.
- Implements timing-attack defense (`dummyBcryptHash`), token generation, token rotation, and token revocation without any `net/http` dependencies.

#### [NEW] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go)
- Unit tests verifying `AuthUseCase` business logic paths.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
- `AuthHandler` refactored to depend strictly on `AuthUseCase`. Handlers decode HTTP requests and format responses.

---

### 3. User Management Domain (`internal/auth`)

#### [NEW] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)
- `UserUseCase` interface:
  - `CreateUser(ctx context.Context, req CreateUserCommand) (*domain.User, error)`
  - `ListUsers(ctx context.Context, tenantID uuid.UUID, page, pageSize int, role string, activeOnly *bool) (*PaginatedUsers, error)`
  - `GetUser(ctx context.Context, tenantID, userID uuid.UUID) (*domain.User, error)`
  - `UpdateUser(ctx context.Context, req UpdateUserCommand) (*domain.User, error)`
  - `DeleteUser(ctx context.Context, actorID, tenantID, targetID uuid.UUID) error`
- Implements transactional audit logging (`UnitOfWork`), email normalization, password strength policy validation (8-72 bytes), and self-deactivation guardrail (`ErrCannotDeactivateSelf`).

#### [NEW] [user_usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go)
- Unit tests for `UserUseCase` testing business validation, audit logging transactions, and self-deactivation guardrails.

#### [NEW] [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go)
- `UserHandler` struct depending on `UserUseCase`. Handles `POST /api/v1/users`, `GET /api/v1/users`, `GET /api/v1/users/{userId}`, `PATCH /api/v1/users/{userId}`, `DELETE /api/v1/users/{userId}`.

---

### 4. Router Wiring & Entrypoint

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- Instantiate `TenantUseCase`, `AuthUseCase`, `UserUseCase`.
- Instantiate `AuthHandler` and `UserHandler`.
- Register routes with `RequireRole` RBAC middleware.

---

## Verification Plan

### Automated Tests
- Run `go test -v -race ./internal/tenant/...`
- Run `go test -v -race ./internal/auth/...`
- Full project test suite: `go test -v -race ./...` ensuring zero data races and 100% pass rate.

### Manual Verification
- Run `docker compose up`.
- Perform `/api/v1/auth/login`, `/api/v1/users/me`, `POST /api/v1/users`, `GET /api/v1/users`.

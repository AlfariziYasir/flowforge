# Phase 2 — Identity, Authentication, and Tenant Safety (TDD-Driven Plan)

This implementation plan outlines the architecture, data models, JWT authentication service, RBAC authorization middleware, API endpoints, **Test-Driven Development (TDD) workflow**, and **architectural recommendations for future improvement** for **Phase 2: Identity, Authentication, and Tenant Safety** as specified in `backlog.md`, `api-1.md`, `api-2.md`, and `CONTEXT.md`.

---

## TDD Concept & Execution Methodology

> [!NOTE]
> **What is Test-Driven Development (TDD)?**
> TDD is an iterative software development technique built on short **Red-Green-Refactor** cycles:
> 1. **RED (Write Test First)**: Write a failing test specifying the desired behavior through a public boundary (a *seam*) before writing any production code. Run the test to confirm it fails for the expected reason.
> 2. **GREEN (Make Test Pass)**: Write the minimal production code necessary to pass the test. No speculative features or unrequested logic.
> 3. **REFACTOR (Clean Up)**: Refactor code and test structure while maintaining green tests.
> 
> **Why Vertical Slicing?**
> Rather than writing all tests upfront or all implementation code upfront, work in **vertical slices** (one seam, one failing test, one minimal implementation at a time). This ensures every line of code is motivated by a test specification.

---

## User Review Required

> [!IMPORTANT]
> - **JWT Signing Algorithm**: HMAC-SHA256 (`golang-jwt/jwt/v5`) using `JWT_SECRET` environment variable with fallback for local dev.
> - **Password Hashing**: `golang.org/x/crypto/bcrypt` with cost 12.
> - **Token Payload**: Access tokens contain `sub` (User ID), `tenantId`, `role`, `iat`, `exp`, `type` (`access`). Refresh tokens contain `sub`, `tenantId`, `exp`, `type` (`refresh`).
> - **Tenant Boundary Enforcement**: `tenant_id` is automatically extracted from JWT context and propagated to DB queries via `BaseRepository[T]`.

---

## TDD Vertical Slices Roadmap

We will execute Phase 2 in 6 sequential **Vertical Slices**. Every slice follows the **Red → Green** loop.

```mermaid
flowchart TD
    subgraph Slice 1: Password Service
        A1[RED: Write password_test.go] --> A2[Verify Failure]
        A2 --> A3[GREEN: Implement password.go] --> A4[Verify Pass]
    end
    subgraph Slice 2: JWT Service
        B1[RED: Write jwt_test.go] --> B2[Verify Failure]
        B2 --> B3[GREEN: Implement jwt.go] --> B4[Verify Pass]
    end
    subgraph Slice 3: Context Helpers
        C1[RED: Write context_test.go] --> C2[Verify Failure]
        C2 --> C3[GREEN: Implement context.go] --> C4[Verify Pass]
    end
    subgraph Slice 4: Auth & RBAC Middleware
        D1[RED: Write middleware_test.go] --> D2[Verify Failure]
        D2 --> D3[GREEN: Implement middleware.go] --> D4[Verify Pass]
    end
    subgraph Slice 5: Repositories Layer
        E1[RED: Write repository_test.go] --> E2[Verify Failure]
        E2 --> E3[GREEN: Implement repositories] --> E4[Verify Pass]
    end
    subgraph Slice 6: HTTP Auth Handlers
        F1[RED: Write handler_test.go] --> F2[Verify Failure]
        F2 --> F3[GREEN: Implement handler.go & main.go] --> F4[Verify Pass]
    end

    Slice 1 --> Slice 2 --> Slice 3 --> Slice 4 --> Slice 5 --> Slice 6
```

---

## Detailed Proposed Changes & Seam Specifications

### Dependencies

#### [MODIFY] [go.mod](file:///home/mohyasiralfarizi/Golang/flowforge/go.mod)
- Install `github.com/golang-jwt/jwt/v5` and `golang.org/x/crypto`.

---

### Vertical Slice 1: Password Service

#### [NEW] [password_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/password_test.go)
- **RED Step**: Write unit tests for password hashing & validation.
  - `TestHashPassword`: Verify plain password hashes into non-empty bcrypt string.
  - `TestComparePassword`: Verify matching password returns `nil`, mismatched password returns error.
- Run `go test ./internal/auth/...` → **FAILS** (Package/types do not exist yet).

#### [NEW] [password.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/password.go)
- **GREEN Step**: Implement `PasswordService` interface & `bcryptPasswordService` struct using `bcrypt.GenerateFromPassword` (cost 12) and `bcrypt.CompareHashAndPassword`.
- Run `go test ./internal/auth/...` → **PASSES**.

---

### Vertical Slice 2: JWT Service

#### [NEW] [jwt_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt_test.go)
- **RED Step**: Write unit tests for JWT creation and validation.
  - `TestGenerateTokenPair`: Verifies valid access token & refresh token string creation.
  - `TestValidateAccessToken_Valid`: Verifies parsing correct claims (`sub`, `tenantId`, `role`).
  - `TestValidateAccessToken_Expired`: Verifies expired tokens are rejected.
  - `TestValidateAccessToken_InvalidSignature`: Verifies tokens signed with wrong secret are rejected.
  - `TestValidateRefreshToken_TypeMismatch`: Verifies access token rejected when expecting refresh token.
- Run `go test ./internal/auth/...` → **FAILS**.

#### [NEW] [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go)
- **GREEN Step**: Implement `jwtService` with `golang-jwt/jwt/v5`.
- Run `go test ./internal/auth/...` → **PASSES**.

---

### Vertical Slice 3: Auth User Context Helpers

#### [NEW] [context_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/context_test.go)
- **RED Step**: Write unit tests for `context.Context` auth values.
  - `TestContextWithAuthUser`: Verifies `AuthUser` injection into context.
  - `TestAuthUserFromContext`: Verifies extraction of `AuthUser` and boolean indicator (`found`).
  - `TestTenantIDFromContext`: Verifies helper returns `TenantID` or `uuid.Nil` if missing.
- Run `go test ./internal/auth/...` → **FAILS**.

#### [NEW] [context.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/context.go)
- **GREEN Step**: Implement `AuthUser` struct and type-safe context helpers.
- Run `go test ./internal/auth/...` → **PASSES**.

---

### Vertical Slice 4: Auth & RBAC Middleware

#### [NEW] [middleware_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware_test.go)
- **RED Step**: Write middleware HTTP tests using `net/http/httptest`.
  - `TestAuthMiddleware_MissingHeader`: Request without `Authorization` header returns `401 Unauthorized`.
  - `TestAuthMiddleware_InvalidBearer`: Request with malformed token returns `401 Unauthorized`.
  - `TestAuthMiddleware_ValidToken`: Request with valid token passes through and injects `AuthUser` into handler context.
  - `TestRequireRole_Allowed`: User with allowed role (e.g. `editor`) passes through.
  - `TestRequireRole_Forbidden`: User with `viewer` role attempting `admin` action returns `403 Forbidden`.
- Run `go test ./internal/auth/...` → **FAILS**.

#### [NEW] [middleware.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go)
- **GREEN Step**: Implement `AuthMiddleware` and `RequireRole(roles ...string)`.
- Run `go test ./internal/auth/...` → **PASSES**.

---

### Vertical Slice 5: Core Domain & Repositories

#### [NEW] [tenant.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/tenant.go)
#### [NEW] [user.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/user.go)
- Core domain structs for Tenant and User matching PostgreSQL DDL.

#### [NEW] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository_test.go)
#### [NEW] [tenant_repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/tenant/repository_test.go)
- **RED Step**: Write unit & repository interface tests.
  - Test `FindByEmail` with tenant scoping.
  - Test `FindBySlug`.
- Run `go test ./...` → **FAILS**.

#### [NEW] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go)
#### [NEW] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/tenant/repository.go)
- **GREEN Step**: Implement `UserRepository` and `TenantRepository` extending generic `postgres.BaseRepository[T]`.
- Run `go test ./...` → **PASSES**.

---

### Vertical Slice 6: Auth HTTP Handlers & Wire-Up

#### [NEW] [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler_test.go)
- **RED Step**: Write HTTP handler tests using `net/http/httptest`.
  - `TestLogin_Success`: Valid email/password returns HTTP 200 with `accessToken` & `refreshToken`.
  - `TestLogin_InvalidCredentials`: Wrong password returns HTTP 401.
  - `TestLogin_InactiveUser`: Inactive user returns HTTP 403.
  - `TestRefresh_Success`: Valid refresh token returns new token pair.
  - `TestLogout_Success`: Returns HTTP 200 with standard success envelope.
  - `TestGetMe_Success`: Protected endpoint returns `AuthUser` profile.
- Run `go test ./internal/auth/...` → **FAILS**.

#### [NEW] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- **GREEN Step**: Implement `AuthHandler`, register routes in `main.go`.
- Run `go test -v -race ./...` → **ALL TESTS PASS**.

---

## Architectural Recommendations for Future Improvements

To transition the User & Auth module from MVP to an enterprise-grade production platform, the following design recommendations should be incorporated in subsequent iterations:

### 1. Atomic User Creation & Audit Logging (Unit of Work)
- **Recommendation**: Wrap user mutations (`CreateUser`, `UpdateRole`, `DeactivateUser`) in `UnitOfWork` transactions (`txManager.ExecuteInTx(ctx, ...)`).
- **Benefit**: Ensures `INSERT INTO users` and `INSERT INTO audit_logs` succeed together or roll back atomically, guaranteeing zero missing audit trails.

### 2. Input Normalization (Email Sanitization)
- **Recommendation**: Sanitize all incoming user emails using `strings.ToLower(strings.TrimSpace(email))` prior to database lookup or creation.
- **Benefit**: Prevents duplicate user accounts and lookup failures caused by casing discrepancies (`User@Domain.com` vs `user@domain.com`).

### 3. Password Policy & Complexity Validation
- **Recommendation**: Add a dedicated password policy validator verifying minimum length (8+ chars), character variety, and capping length to 72 bytes (preventing bcrypt truncation issues).
- **Benefit**: Protects against weak passwords and bcrypt-targeted Denial of Service (DoS) attacks.

### 4. Last Login Timestamp & Security Auditing
- **Recommendation**: Track `last_login_at` in the `users` table, updated asynchronously or within the login handler.
- **Benefit**: Enables tenant administrators to monitor inactive users and perform security auditing.

### 5. Domain Sentinel Error Catalog
- **Recommendation**: Expand domain sentinel errors (`ErrUserAlreadyExists`, `ErrUserInactive`, `ErrInvalidRole`).
- **Benefit**: Decouples database driver errors from HTTP handlers and produces consistent REST API error codes (`USER_ALREADY_EXISTS`, `AUTH_FORBIDDEN`).

---

## Verification Plan

### Automated Tests
- Execution of each RED step to observe exact failure.
- Execution of each GREEN step to observe exact pass.
- Final test suite check: `go test -v -race ./...` verifying zero data races and 100% passing tests.

### Manual Verification
- Launch application via `docker compose up`.
- Perform HTTP request to `POST /api/v1/auth/login` with seeded credentials (`admin@flowforge.local`).
- Use returned token in `Authorization: Bearer <token>` to request `GET /api/v1/users/me`.

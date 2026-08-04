# Implementation Plan — Deep Code Review Remediation

This plan remediates all 19 findings identified in `.agents/plans/deep_code_review_findings.md`, covering critical bugs, security vulnerabilities, architectural layer violations, and multi-tenant schema constraints.

---

## User Review Required

> [!IMPORTANT]
> - **C-1 Fix (`BaseRepository.Create`)**: Replaces `.Values(setMap)` with `.SetMap(setMap)` in `BaseRepository.Create` to prevent invalid SQL runtime crashes.
> - **C-2 & M-1 Fix (JWT Claims)**: Eliminates JSON field tag collision between `CustomClaims.UserID` (`sub`) and `RegisteredClaims.Subject` by using `RegisteredClaims.Subject` as the canonical subject string and `RegisteredClaims.ID` as canonical `jti`.
> - **S-1 Fix (Blacklist Fail-Closed)**: Enforces fail-closed token revocation checks so Redis outages do not allow revoked tokens to bypass authentication.
> - **S-3 & S-5 Fixes (Security Hardening)**: Enforces a 1 MB request body limit via `http.MaxBytesReader` across HTTP handlers and fixes URL credential redaction for encoded password characters.

---

## Proposed Changes

### Platform & Persistence Layer (`internal/platform`)

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go)
- In `BaseRepository.Create`: Replace `.Values(setMap)` with `.SetMap(setMap)`.
- In `repository_test.go`: Add test case verifying `BaseRepository.Create`.

#### [MODIFY] [redact.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/logger/redact.go)
- Update `RedactURL` to use `url.UserPassword(u.User.Username(), "*****")` and `u.String()` to handle URL-encoded password characters properly.
- Update `redact_test.go` with test cases for URL-encoded characters (e.g. `p%40ss`).

#### [MODIFY] [config.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/config/config.go)
- Add `JWTSecret` field to `Config` struct loaded via `getEnv("JWT_SECRET", ...)` to centralize environment configuration.

---

### Security & Auth Domain (`internal/auth`)

#### [MODIFY] [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go)
- Refactor `CustomClaims`: Remove `UserID` (`sub`) and `JTI` (`jti`) fields from `CustomClaims`. Use `RegisteredClaims.Subject` and `RegisteredClaims.ID`.
- Add helper methods `(c *CustomClaims) UserID() uuid.UUID` and `(c *CustomClaims) JTI() string`.
- In `NewJWTService`: Validate `secretKey` length $\ge 32$ chars when creating service.
- Update `jwt_test.go`.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)
- **S-1 Fail-Closed Blacklist**: Change `IsRevoked` check in `Refresh` and `Logout` to fail-closed on Redis error:
  ```go
  revoked, err := u.blacklist.IsRevoked(ctx, claims.JTI())
  if err != nil {
      return nil, fmt.Errorf("check token revocation: %w", err)
  }
  if revoked {
      return nil, ErrUnauthorized
  }
  ```
- **S-4 Dynamic `dummyBcryptHash`**: Replace hardcoded `dummyBcryptHash` constant with dynamically generated `bcrypt` hash in `init()`.
- **A-1 Transport Decoupling**: Remove `strings.TrimPrefix(tokenStr, "Bearer ")` from `Logout` usecase. Expect clean token string.

#### [MODIFY] [middleware.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go)
- Update `Authenticate` middleware to check `IsRevoked` with fail-closed error handling.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
- **S-2 `GetMe` Error Handling**: Return `404 Not Found` if user doesn't exist, `500 Internal Error` on DB error. Remove fallback `200 OK` with stale JWT.
- **S-3 Body Limit**: Add `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)` (1 MB limit) to HTTP handlers.
- **A-1 Bearer Stripping**: Clean `Bearer ` prefix from `Authorization` header in `Logout` handler before passing to `AuthUseCase.Logout`.

#### [MODIFY] [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go)
- Add `http.MaxBytesReader` request body protection to user endpoints.

#### [MODIFY] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository_test.go)
- **A-3 Mock Lookup Fix**: Refactor `MockUserRepository` to index users by `user.ID` (UUID) so updating a user's email does not produce stale-key lookup failures.

---

### Database Schema Migration (`migrations/`)

#### [MODIFY] [000001_init_schema.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql)
- **D-1 & D-2 Composite Foreign Keys**: Add `tenant_id` composite foreign keys for `step_runs`, `execution_logs`, and `audit_logs` tables to guarantee multi-tenant safety across all database relationships.

---

## Verification Plan

### Automated Tests
- Run `go test -v -race ./...` across all packages (`internal/platform/...`, `internal/tenant/...`, `internal/auth/...`, `cmd/...`) verifying zero data races and 100% pass rate.

### Regression Checks
- Verify `BaseRepository.Create` works with real SQL queries.
- Verify JWT claim marshaling and unmarshaling retains valid `Subject` UUID and `ID` JTI.
- Verify Redis failure simulation in test returns error instead of bypassing revocation check.

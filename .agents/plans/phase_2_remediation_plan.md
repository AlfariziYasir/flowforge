# Phase 2 Remediation Plan — Security, Revocation & Audit Logging

This remediation plan addresses the findings from the Phase 2 code review and security audit. It outlines technical fixes for JWT secret security, Redis-backed token revocation, transactional audit logging for auth events, login timing attack mitigation, and code style optimizations.

---

## User Review Required

> [!IMPORTANT]
> - **Token Revocation Strategy**: Uses Redis (`internal/platform/redis`) to maintain a blacklist of revoked JWT `jti` (JWT ID) claims upon user logout or forced session termination.
> - **Audit Logging**: Authentication events (`user.login.success`, `user.login.failed`, `user.logout`, `token.refresh`) will be persisted to the `audit_logs` table via `UnitOfWork` or dedicated audit logger.
> - **Production Guardrail**: The application will fail at startup if `JWT_SECRET` is unset or less than 32 characters in production/staging environments.

---

## Open Questions

- None at present. All remediation items align with the system architecture in `CONTEXT.md` and standard security best practices.

---

## Proposed Changes

### Component 1: Security & JWT Service

#### [MODIFY] [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go)
- Add `JTI` (`json:"jti"`) claim to `CustomClaims`.
- Generate unique `uuid.New().String()` as `jti` for both access and refresh tokens.
- Clean up duplicate `"sub"` JSON tag between `UserID` and embedded `RegisteredClaims.Subject`.
- Add validation in `NewJWTService` / startup check to ensure secret length $\ge 32$ bytes in non-development modes.

#### [MODIFY] [jwt_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt_test.go)
- Update unit tests to verify `jti` claim generation and validation.

---

### Component 2: Token Revocation Service (Redis)

#### [NEW] [token_blacklist.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/token_blacklist.go)
- Implement `TokenBlacklist` interface backed by `redis.Client` (`internal/platform/redis`).
- Methods: `Revoke(ctx context.Context, jti string, expiration time.Duration) error` and `IsRevoked(ctx context.Context, jti string) (bool, error)`.

#### [NEW] [token_blacklist_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/token_blacklist_test.go)
- Unit tests using miniredis / redis client mocks.

---

### Component 3: HTTP Auth Handlers & Middleware

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
- **Timing Attack Defense**: Perform dummy bcrypt comparison when tenant or user is not found during `Login()` to equalize response time (~100ms) and eliminate email enumeration vulnerability.
- **Logout Revocation**: In `Logout()`, parse the access token / refresh token `jti` and add to `TokenBlacklist`.
- **Refresh Check**: Check `TokenBlacklist.IsRevoked` before issuing new token pairs.
- **Audit Logging**: Write audit records to `audit_logs` table for successful and failed auth attempts.

#### [MODIFY] [middleware.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go)
- Check `TokenBlacklist.IsRevoked(ctx, claims.JTI)` in `Authenticate` middleware to instantly invalidate revoked access tokens.

---

### Component 4: Application Bootstrap & Config Guardrails

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- Inject Redis `TokenBlacklist` into `AuthMiddleware` and `AuthHandler`.
- Add startup assertion validating `JWTSecret` strength.

---

## Verification Plan

### Automated Tests
- `go test -v -race ./internal/auth/...`
- Unit tests covering token revocation, timing attack dummy comparison, and `jti` claim extraction.

### Manual Verification
- Perform `POST /api/v1/auth/login` to obtain access/refresh token pair.
- Perform `POST /api/v1/auth/logout` with the token.
- Attempt to use the revoked token in `GET /api/v1/users/me` -> Verify `401 Unauthorized`.

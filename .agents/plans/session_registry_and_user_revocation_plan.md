# Implementation Plan — User Revocation Timestamp & Redis Session Registry

This plan introduces an enterprise-grade **Session Registry** and **User Revocation Timestamp** mechanism in Redis for the FlowForge Auth Domain (`internal/auth`), closing security gaps around instant multi-device revocation, user suspension enforcement, and active session tracking without sacrificing performance.

---

## User Review Required

> [!IMPORTANT]
> - **Fail-Closed Security**: Redis failures during revocation checks will reject requests (`401 Unauthorized`) to prevent unauthorized access during outages.
> - **JWT Claims Schema Update**: `CustomClaims` will include `sessionId` (`sid`). Existing active tokens without `sid` will be gracefully handled during migration.
> - **Clean Architecture Layering**: Zero HTTP/Web framework logic in `AuthUseCase` or `SessionStore`. Contextual details (`ipAddress`, `userAgent`) are passed down from `AuthHandler` / `AuthMiddleware`.

---

## Technical Architecture & Redis Keys

```mermaid
flowchart TD
    subgraph Client Request
        Req[HTTP Request + Bearer JWT]
    end

    subgraph AuthMiddleware (Delivery Layer)
        M1[Validate JWT Signature]
        M2[Check JTI Blacklist]
        M3[Check User Revocation Timestamp: user:revoked_before:userID]
        M4[Check Session Status: session:tenantID:userID:sessionID]
    end

    subgraph Redis Store (Platform Layer)
        R1[token:revoked:JTI]
        R2[user:revoked_before:userID]
        R3[session:tenantID:userID:sessionID]
    end

    Req --> M1
    M1 -->|Valid| M2
    M2 -->|Not Revoked| M3
    M3 -->|Issued After Revocation| M4
    M4 -->|Session Active| Next[Proceed to Handler]

    M2 -. Check .-> R1
    M3 -. Check .-> R2
    M4 -. Check .-> R3
```

### Redis Key Patterns & Schema

1. **User Revocation Key**: `user:revoked_before:{user_id}`
   - Value: Unix timestamp integer (e.g. `1753704123`).
   - TTL: Set to max refresh token expiration (e.g. `7 days`).
   - Purpose: Instantly invalidates **ALL** tokens issued prior to timestamp (e.g. on Password Reset, Account Suspension, or Global Logout).

2. **Session Registry Key**: `session:{tenant_id}:{user_id}:{session_id}`
   - Value: JSON payload:
     ```json
     {
       "sessionId": "uuid",
       "userId": "uuid",
       "tenantId": "uuid",
       "ipAddress": "192.168.1.1",
       "userAgent": "Mozilla/5.0...",
       "createdAt": "2026-07-28T19:00:00Z",
       "lastActiveAt": "2026-07-28T19:05:00Z",
       "expiresAt": "2026-08-04T19:00:00Z"
     }
     ```
   - TTL: Equal to Refresh Token duration (e.g. `7 days`).

---

## Proposed Changes

### Auth Domain (`internal/auth`)

#### [MODIFY] [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go)
- Add `SessionID uuid.UUID` to `CustomClaims` struct (`json:"sid,omitempty"`).
- Add `SessionID()` getter helper to `CustomClaims`.
- Update `JWTService.GenerateTokenPair` signature to accept `sessionID uuid.UUID`.

#### [NEW] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)
- Define `UserSession` struct (`SessionID`, `UserID`, `TenantID`, `IPAddress`, `UserAgent`, `CreatedAt`, `LastActiveAt`, `ExpiresAt`).
- Define `SessionStore` interface:
  - `CreateSession(ctx context.Context, session *UserSession, ttl time.Duration) error`
  - `GetSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) (*UserSession, error)`
  - `RevokeSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) error`
  - `RevokeAllUserSessions(ctx context.Context, tenantID, userID uuid.UUID) error`
  - `SetUserRevokedBefore(ctx context.Context, userID uuid.UUID, revokedAt time.Time, ttl time.Duration) error`
  - `IsUserRevoked(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error)`
  - `ListUserSessions(ctx context.Context, tenantID, userID uuid.UUID) ([]*UserSession, error)`
- Implement `redisSessionStore` backed by `go-redis/v9`.
- Implement `noopSessionStore` for unit tests and fallback.

#### [NEW] [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go)
- Unit tests verifying session creation, key prefix formatting, revocation timestamp checks, and JSON serialization.

#### [MODIFY] [middleware.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go)
- Integrate `SessionStore` into `AuthMiddleware`.
- In `Authenticate()`:
  - Check `SessionStore.IsUserRevoked(ctx, claims.UserID(), claims.IssuedAt.Time)`.
  - Check `SessionStore.GetSession(ctx, claims.TenantID, claims.UserID(), claims.SessionID())` if `SessionID` is present.
  - Return `401 Unauthorized` on fail-closed error or revoked session.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)
- Inject `SessionStore` into `authUseCase`.
- `Login`: Generate `sessionID = uuid.New()`, store `UserSession` in Redis, generate token pair with `sessionID`.
- `Refresh`: Verify active session in Redis, update `lastActiveAt`.
- `Logout`: Call `SessionStore.RevokeSession(...)` and blacklist `jti`.
- Add `LogoutAllDevices(ctx context.Context, tenantID, userID uuid.UUID) error`: sets `user:revoked_before:{userID}` timestamp and deletes all active session keys.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
- Capture IP Address (`r.RemoteAddr` / `X-Forwarded-For`) and User-Agent (`r.UserAgent()`) in `Login` request and pass to `AuthUseCase`.
- Add `POST /api/v1/auth/logout-all` endpoint handler.

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)
- On `UpdateUser` (when `IsActive` set to `false`) or Password Reset: trigger `SessionStore.SetUserRevokedBefore` and `RevokeAllUserSessions`.

---

## Verification Plan

### Automated Tests
- Run Mockery mock generation: `make mocks`
- Unit tests: `go test -v -race ./internal/auth/...`
- Integration tests: `go test -v -race ./internal/platform/postgres/... ./cmd/api/...`
- Full test suite execution: `go test -v -race ./...` ensuring 0 race conditions.

### Manual Verification
- Test `POST /api/v1/auth/login` and verify Redis keys `session:{tenant_id}:{user_id}:{session_id}`.
- Test `POST /api/v1/auth/logout-all` and verify `user:revoked_before:{user_id}` blocks subsequent requests with previous JWT tokens.

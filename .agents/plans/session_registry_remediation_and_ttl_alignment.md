# Implementation Plan — TTL Configuration Alignment & Paginated Session Listing Endpoint

This implementation plan executes the two audit recommendations for the FlowForge Auth Domain (`internal/auth` & `internal/platform/config`):
1. **TTL Configuration Alignment**: Configurable `JWTRefreshExpiry` dynamically passed into `AuthUseCase` and `SessionStore` (eliminating hardcoded 7-day TTL defaults).
2. **Paginated Session Listing Endpoint**: Upgrading `SessionStore.ListUserSessions` to support offset-based pagination and adding `GET /api/v1/auth/sessions` API endpoint.

---

## User Review Required

> [!IMPORTANT]
> - **Config Additions**: `JWTAccessExpiry` (default `15m`) and `JWTRefreshExpiry` (default `7d` / `168h`) added to `config.Config`.
> - **New Authenticated API Endpoint**: `GET /api/v1/auth/sessions?page=1&pageSize=20` to view active login sessions for the authenticated user.
> - **Clean Architecture**: `AuthUseCase` handles business logic and pagination validation; `SessionStore` handles Redis scanning and slicing; `AuthHandler` handles HTTP query param parsing.

---

## Proposed Changes

### 1. Platform Configuration (`internal/platform/config`)

#### [MODIFY] [config.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/config/config.go)
- Add `JWTAccessExpiry time.Duration` (default `15 * time.Minute`) and `JWTRefreshExpiry time.Duration` (default `7 * 24 * time.Hour`) to `Config` struct.
- Parse `JWT_ACCESS_EXPIRY` (e.g. `15m`) and `JWT_REFRESH_EXPIRY` (e.g. `168h`) environment variables using `time.ParseDuration`.

---

### 2. Auth Domain (`internal/auth`)

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)
- Update `SessionStore` interface:
  ```go
  ListUserSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) ([]*UserSession, int64, error)
  ```
- Implement `ListUserSessions` in `redisSessionStore`:
  - Scan keys for `session:{tenant_id}:{user_id}:*`.
  - Calculate total count.
  - Apply pagination slice `[(page-1)*pageSize : min(page*pageSize, total)]`.
  - Fetch and unmarshal only the paginated slice of sessions.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)
- Add `refreshExpiry time.Duration` to `authUseCase` struct and `NewAuthUseCase` constructor.
- Use `u.refreshExpiry` in `Login`, `Refresh`, `LogoutAllDevices`, and `CreateSession`.
- Add `PaginatedSessions` struct:
  ```go
  type PaginatedSessions struct {
      Sessions []*UserSession `json:"sessions"`
      Total    int64          `json:"total"`
      Page     int            `json:"page"`
      Size     int            `json:"pageSize"`
  }
  ```
- Add `ListSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) (*PaginatedSessions, error)` to `AuthUseCase` interface and `authUseCase` struct.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)
- Add `ListSessions(w http.ResponseWriter, r *http.Request)` handler method parsing `page` and `pageSize` query string parameters.

---

### 3. Entrypoint Router Wiring (`cmd/api/main.go`)

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
- Use `cfg.JWTAccessExpiry` and `cfg.JWTRefreshExpiry` when initializing `auth.NewJWTService` and `auth.NewAuthUseCase`.
- Register `GET /api/v1/auth/sessions` in `NewRouter` under `authMiddleware.Authenticate`.

---

## Verification Plan

### Automated Tests
- Regenerate Mockery mocks: `make mocks`
- Run unit tests: `go test -v -race ./internal/platform/config/... ./internal/auth/...`
- Full workspace verification: `go test -v -race ./...`

### Manual Verification
- Test `GET /api/v1/auth/sessions?page=1&pageSize=5` using Bearer JWT authentication and verify paginated response.

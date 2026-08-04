# Deep Code Review — FlowForge Implementation

Full-depth review of the current implementation covering **real bugs**, **security vulnerabilities**, **architecture violations**, and **schema integrity issues**. Findings ordered by severity.

---

## 🔴 Critical Bugs

### C-1: `BaseRepository.Create` generates broken SQL

[repository.go:171-196](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L171-L196)

```go
setMap, err := structToMap(entity) // returns map[string]any
query, args, err := StatementBuilder.
    Insert(r.tableName).
    Values(setMap).   // ← BUG: Values() expects positional values, NOT a map
    ToSql()
```

Squirrel's `.Values(...)` takes `...interface{}` positional values — each arg is one column's value. Passing a `map[string]any` as a single argument inserts the entire map as one value, producing invalid SQL like `INSERT INTO users VALUES ($1)` where `$1` is a Go map.

**Fix:** Replace `.Values(setMap)` with `.SetMap(setMap)`, which correctly decomposes the map into `Columns(...)` + `Values(...)`.

> [!WARNING]
> This is a **latent runtime bug**. Current code bypasses it because [repository.go:90-119](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go#L90-L119) (`CreateUser`) writes its own custom INSERT. But any future feature calling `BaseRepository.Create` directly will crash at runtime.

---

### C-2: JWT `sub` claim collision between `UserID` and `RegisteredClaims.Subject`

[jwt.go:31-39](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go#L31-L39)

```go
type CustomClaims struct {
    UserID    uuid.UUID `json:"sub"`     // ← maps to "sub"
    // ...
    jwt.RegisteredClaims                  // ← RegisteredClaims.Subject also maps to "sub"
}
```

And during token creation ([jwt.go:71-78](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go#L71-L78)):
```go
accessClaims := CustomClaims{
    UserID: userID,          // sets json:"sub" as UUID
    RegisteredClaims: jwt.RegisteredClaims{
        Subject: userID.String(),  // also sets "sub" as string
    },
}
```

**What happens:**
- **Marshaling (token creation):** Go's JSON rules give priority to the outer (non-embedded) field. `UserID` wins, so `"sub"` is serialized as a UUID string. ✅ Works by accident.
- **Unmarshaling (token parsing):** The `"sub"` value is decoded into `UserID` (uuid.UUID). `RegisteredClaims.Subject` is left **empty**. ❌
- **Impact:** Any code that uses `claims.Subject` or standard JWT validation features (e.g., `jwt.WithSubject()`) will silently get empty strings and fail.

**Fix:** Remove `UserID uuid.UUID \`json:"sub"\`` and use only `RegisteredClaims.Subject`. Parse the UUID from `claims.Subject` in application code, OR remove the `Subject` field from `RegisteredClaims` and rely solely on the custom `UserID`.

---

## 🟠 Security Vulnerabilities

### S-1: Blacklist check **fails-open** — revoked tokens become valid when Redis is down

[usecase.go:126-130](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L126-L130)

```go
if claims.JTI != "" {
    revoked, err := u.blacklist.IsRevoked(ctx, claims.JTI)
    if err == nil && revoked {   // ← If err != nil (Redis down), check is SKIPPED
        return nil, ErrUnauthorized
    }
}
```

Same issue in [middleware.go:58-64](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L58-L64).

**Impact:** If Redis goes down, **every previously revoked token becomes valid again**. An attacker who has captured a "logged out" token can use it freely during Redis outages.

**Fix:** Fail-closed. Return an error when the blacklist check fails:
```go
revoked, err := u.blacklist.IsRevoked(ctx, claims.JTI)
if err != nil {
    return nil, fmt.Errorf("check token revocation: %w", err)
}
if revoked {
    return nil, ErrUnauthorized
}
```

---

### S-2: `GetMe` handler masks database errors and returns stale JWT data as 200 OK

[handler.go:112-116](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L112-L116)

```go
user, err := h.authUC.GetMe(r.Context(), authUser.TenantID, authUser.ID)
if err != nil {
    respondJSON(w, http.StatusOK, authUser)  // ← Returns stale JWT claims on ANY error
    return
}
```

**Impact:**
- If user was **deactivated** or **deleted** after JWT was issued → client sees 200 OK with stale data, never learns they're locked out.
- If DB is **unreachable** → error is swallowed; client gets incorrect 200 instead of 500.
- No logging of the error, making debugging impossible.

**Fix:** Differentiate error types:
```go
if errors.Is(err, ErrUserNotFound) {
    respondJSONError(w, http.StatusNotFound, "Not Found", "user no longer exists")
    return
}
if err != nil {
    respondJSONError(w, http.StatusInternalServerError, "Internal Error", "failed to retrieve profile")
    return
}
```

---

### S-3: No HTTP request body size limit — OOM vector

All handlers in [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go) and [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go) use:

```go
json.NewDecoder(r.Body).Decode(&req)
```

Without `http.MaxBytesReader`, an attacker can send a multi-GB JSON payload to exhaust server memory.

**Fix:** Wrap `r.Body` before decoding:
```go
r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
```

---

### S-4: `dummyBcryptHash` timing-attack defense is fragile

[usecase.go:19](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L19)

```go
const dummyBcryptHash = "$2a$12$e0M2/h3uA.g.0Sg3/7aG9u4nU6b3e.6V1s/8c7e.6V1s/8c7e.6V1"
```

This is a hand-crafted hash string. While it happens to be 60 chars and uses valid bcrypt base64 characters, it was NOT generated by bcrypt. If Go's bcrypt internals ever change how they validate the hash structure before comparing, this could return immediately (non-constant-time), defeating the defense.

**Fix:** Generate a real dummy hash at init time:
```go
var dummyBcryptHash string
func init() {
    h, _ := bcrypt.GenerateFromPassword([]byte("flowforge-dummy-never-used"), 12)
    dummyBcryptHash = string(h)
}
```

---

### S-5: `RedactURL` can leak passwords with URL-encoded characters

[redact.go:19-21](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/logger/redact.go#L19-L21)

```go
if pass, hasPassword := u.User.Password(); hasPassword && pass != "" {
    return strings.Replace(rawURL, ":"+pass+"@", ":*****@", 1)
}
```

`url.Parse` URL-decodes the password, but `rawURL` still has the encoded form. For a URL like `postgres://user:p%40ss@host`, `pass` becomes `p@ss` (decoded), but the raw string contains `:p%40ss@`. The `strings.Replace` won't match, and the **password is logged in plain text**.

**Fix:** Use `u.Redacted()` or reconstruct the URL:
```go
u.User = url.UserPassword(u.User.Username(), "*****")
return u.String()
```

---

## 🟡 Architecture & Logic Issues

### A-1: `Logout` does Bearer-prefix stripping in the UseCase layer (HTTP transport leak)

[usecase.go:167-168](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L167-L168)

```go
tokenStr = strings.TrimPrefix(tokenStr, "Bearer ")
tokenStr = strings.TrimSpace(tokenStr)
```

The use case should receive a clean token string. The handler at [handler.go:95](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L95) passes the raw `Authorization` header directly. This violates Clean Architecture: HTTP-specific concerns (header parsing) should be handled in the transport/delivery layer.

**Fix:** Move Bearer stripping to the handler, pass only the raw token to the use case.

---

### A-2: `DeleteUser` is actually soft-delete (deactivation) — naming mismatch

[user_usecase.go:146-162](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L146-L162)

```go
func (u *userUseCase) DeleteUser(...) error {
    usr.IsActive = false          // ← Sets inactive
    return u.userRepo.UpdateUser(ctx, usr)  // ← Calls UPDATE, not DELETE
}
```

But the handler responds with `"user deleted successfully"`. This is misleading — the record still exists. If someone later implements actual hard-delete, the naming collision will cause confusion.

**Fix:** Either rename to `DeactivateUser` (preferred) or actually implement a hard delete (with appropriate data retention policy).

---

### A-3: `UpdateUser` mock has a stale-key bug when email changes

[repository_test.go:57-64](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository_test.go#L57-L64)

```go
func (m *MockUserRepository) UpdateUser(ctx context.Context, user *domain.User) error {
    sanitizedEmail := strings.ToLower(strings.TrimSpace(user.Email))
    key := user.TenantID.String() + ":" + sanitizedEmail
    if _, exists := m.users[key]; !exists {  // ← Looks up by NEW email
        return auth.ErrUserNotFound
    }
    m.users[key] = user
    return nil
}
```

If a user's email is changed from `old@x.com` to `new@x.com`, the mock looks up `tenantID:new@x.com`, which doesn't exist, and returns `ErrUserNotFound`. The old key is never cleaned up either, leaving a dangling entry. This mock will **break any test that changes a user's email**.

**Fix:** Use the user's `ID` as the primary lookup key (matching the real repository behavior), not the email.

---

## 🔵 Schema Integrity Issues

### D-1: `step_runs` uses simple FKs, breaking multi-tenant isolation

[000001_init_schema.up.sql:137-138](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql#L137-L138)

```sql
workflow_run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
workflow_node_id UUID NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
```

Every other child table (workflow_versions, workflow_nodes, workflow_edges, workflow_runs) uses **composite FKs** that include `tenant_id`, but `step_runs` uses **simple FKs**. This means a step_run in tenant A could theoretically reference a workflow_run in tenant B, breaking tenant isolation at the database level.

**Fix:** Add composite FK constraints:
```sql
CONSTRAINT fk_step_runs_run FOREIGN KEY (tenant_id, workflow_run_id)
    REFERENCES workflow_runs(tenant_id, id) ON DELETE CASCADE,
CONSTRAINT fk_step_runs_node FOREIGN KEY (tenant_id, workflow_node_id)
    REFERENCES workflow_nodes(tenant_id, workflow_version_id, id) ON DELETE CASCADE
```

(Requires adding corresponding unique constraints on the referenced tables.)

---

### D-2: `execution_logs` and `audit_logs` also use simple FKs

[000001_init_schema.up.sql:159-160](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql#L159-L160) and [L175](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql#L175):

Same pattern — `workflow_run_id`, `step_run_id`, `actor_user_id` all use simple FKs without `tenant_id` composite enforcement.

---

## ⚪ Minor / Style Issues

| # | File | Line | Issue |
|---|------|------|-------|
| M-1 | [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go#L32-L33) | 32-33 | `JTI` is set as both `CustomClaims.JTI` and `RegisteredClaims.ID` — same duplication pattern as `sub`. Consolidate to one. |
| M-2 | [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go#L55-L61) | 55-61 | `NewJWTService` doesn't validate inputs. A zero-length `secretKey` or zero `accessExpiry` would produce broken tokens silently. |
| M-3 | [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L177) | 177 | `_ = u.blacklist.Revoke(...)` in `Logout` — silent error swallowing means logout can silently fail to revoke the token. Consider logging the error. |
| M-4 | [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L121-L125) | 121-125 | `respondJSON` ignores `json.Encode` error. If the response body write fails, the status code is already sent. Consider logging. |
| M-5 | [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L185) | 185 | `JWT_SECRET` loaded via `os.Getenv` directly instead of through `config.Load()`. Splits secret management across two mechanisms. |
| M-6 | [config.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/config/config.go#L23) | 23 | Default `DATABASE_URL` contains hardcoded credentials `postgres:postgres`. While dev-only, this should be documented or generate a warning. |
| M-7 | All handlers | — | No `Content-Type` validation on request. Non-JSON payloads produce confusing "invalid JSON body" errors instead of "unsupported media type". |

---

## Summary Matrix

| Severity | Count | IDs |
|----------|-------|-----|
| 🔴 Critical Bug | 2 | C-1, C-2 |
| 🟠 Security | 5 | S-1, S-2, S-3, S-4, S-5 |
| 🟡 Architecture | 3 | A-1, A-2, A-3 |
| 🔵 Schema | 2 | D-1, D-2 |
| ⚪ Minor | 7 | M-1 through M-7 |

> [!IMPORTANT]
> **Top priority:** Fix **S-1** (fail-open blacklist) and **C-1** (broken `BaseRepository.Create`) first, as these are the highest-impact issues — one is a security hole, the other is a latent runtime crash.

Shall I create a remediation plan with specific code changes for these findings?

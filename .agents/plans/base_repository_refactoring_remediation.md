# Remediation Plan — BaseRepository Bugs + Mockery Integration

Combines the 8 bug fixes with migration from hand-written mocks to [vektra/mockery](https://github.com/vektra/mockery) generated mocks.

---

## Phase 1: Fix BaseRepository Bugs

### Platform Layer

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go)

**B-1:** Wrap `filters` in `sq.Eq` inside `FindByFilter` (line 46):

```diff
-		Where(filters).
+		Where(sq.Eq(filters)).
```

**B-5:** Fix copy-paste error messages in `Update` (lines 215, 221):

```diff
-		return fmt.Errorf("failed to build create sql: %w", err)
+		return fmt.Errorf("failed to build update sql: %w", err)
```
```diff
-		return fmt.Errorf("failed to create %s record: %w", r.tableName, err)
+		return fmt.Errorf("failed to update %s record: %w", r.tableName, err)
```

**B-6:** Return `domain.ErrNotFound` instead of generic string (line 225):

```diff
-		return errors.New("data not found")
+		return domain.ErrNotFound
```

---

#### [MODIFY] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go)

**B-7:** Fix `FindByID` → `FindByFilter` and `Paginate` signature:

```diff
-		user, err := repo.FindByID(ctx, tenantID, userID)
+		user, err := repo.FindByFilter(ctx, map[string]interface{}{"tenant_id": tenantID, "id": userID})
```

```diff
-		user, err := repo.FindByID(ctx, tenantID, "00000000-...")
+		user, err := repo.FindByFilter(ctx, map[string]interface{}{"tenant_id": tenantID, "id": "00000000-..."})
```

```diff
-		users, total, err := repo.Paginate(ctx, tenantID, postgres.PaginationParams{
+		users, total, err := repo.Paginate(ctx, postgres.PaginationParams{
 			Page:     1,
 			PageSize: 10,
 			OrderBy:  "created_at DESC",
+			Filters:  map[string]interface{}{"tenant_id": tenantID},
 		})
```

---

### Auth Layer

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go)

**B-4:** Translate `domain.ErrNotFound` → `ErrUserNotFound` in both `FindByEmail` and `FindByID`:

```diff
 	user, err := r.base.FindByFilter(ctx, filters)
 	if err != nil {
+		if errors.Is(err, domain.ErrNotFound) {
+			return nil, ErrUserNotFound
+		}
 		return nil, err
 	}
```

> [!IMPORTANT]
> Add `"errors"` to the import block if not present, and ensure `"flowforge/internal/domain"` is imported.

**B-2 + B-3:** Fix `ListUsers` nil-panic and conditional filters:

```diff
-	if *isAsc {
-		params.OrderBy = fmt.Sprintf("%s asc", orderby)
-	} else {
-		params.OrderBy = fmt.Sprintf("%s desc", orderby)
+	if orderby != "" {
+		direction := "desc"
+		if isAsc != nil && *isAsc {
+			direction = "asc"
+		}
+		params.OrderBy = fmt.Sprintf("%s %s", orderby, direction)
 	}
 
-	params.Filters = map[string]interface{}{
-		"tenant_id": tenantID.String(),
-		"role":      role,
-		"is_active": activeOnly,
+	params.Filters = map[string]interface{}{
+		"tenant_id": tenantID.String(),
+	}
+	if role != "" {
+		params.Filters["role"] = role
+	}
+	if activeOnly != nil {
+		params.Filters["is_active"] = *activeOnly
 	}
```

---

#### [MODIFY] [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go)

**B-8:** Parse `orderBy` and `isAsc` from query params in `ListUsers`:

```diff
 	pageSize, _ := strconv.Atoi(queryVals.Get("pageSize"))
 	role := queryVals.Get("role")
+	orderBy := queryVals.Get("orderBy")
 
 	var activeOnly *bool
 	// ...
+	var isAsc *bool
+	if ascStr := queryVals.Get("isAsc"); ascStr != "" {
+		if val, err := strconv.ParseBool(ascStr); err == nil {
+			isAsc = &val
+		}
+	}
 
 	query := ListUsersQuery{
 		// ...
+		OrderBy:    orderBy,
+		IsAsc:      isAsc,
```

---

## Phase 2: Mockery Integration

### Setup

#### [NEW] [Makefile](file:///home/mohyasiralfarizi/Golang/flowforge/Makefile)

```makefile
.PHONY: mocks test build vet

# Install mockery if not present
install-mockery:
	go install github.com/vektra/mockery/v2@latest

# Generate mocks from .mockery.yaml
mocks:
	mockery

# Run tests
test:
	go test ./internal/... -race -count=1 -v

# Build
build:
	go build ./...

# Vet
vet:
	go vet ./...
```

---

#### [NEW] [.mockery.yaml](file:///home/mohyasiralfarizi/Golang/flowforge/.mockery.yaml)

```yaml
with-expecter: true
packages:
  flowforge/internal/auth:
    config:
      dir: "internal/auth/mocks"
      outpkg: "mocks"
    interfaces:
      UserRepository:
      AuthUseCase:
      UserUseCase:
      JWTService:
      PasswordService:
      TokenBlacklist:
  flowforge/internal/tenant:
    config:
      dir: "internal/tenant/mocks"
      outpkg: "mocks"
    interfaces:
      TenantRepository:
      TenantUseCase:
```

This will auto-generate mock files in:
- `internal/auth/mocks/mock_UserRepository.go`
- `internal/auth/mocks/mock_AuthUseCase.go`
- `internal/auth/mocks/mock_JWTService.go`
- `internal/auth/mocks/mock_PasswordService.go`
- `internal/auth/mocks/mock_TokenBlacklist.go`
- `internal/auth/mocks/mock_UserUseCase.go`
- `internal/tenant/mocks/mock_TenantRepository.go`
- `internal/tenant/mocks/mock_TenantUseCase.go`

---

### Migrate Tests to Mockery Mocks

#### [MODIFY] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository_test.go)

**Delete** all hand-written mock structs (`MockUserRepository`, `NewMockUserRepository`, and all methods). Keep only the `TestUserRepository_*` test functions, but update them to use mockery mocks.

> [!IMPORTANT]
> The `TestUserRepository_*` tests validate in-memory mock logic, not real DB queries. Once we switch to mockery mocks (which use `mock.On()`/`Return()` expectations), these tests become integration tests for repository behavior. We'll keep them as **pure unit tests** with expectation-based mocks.

---

#### [MODIFY] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go)

**Delete** `mockTenantUseCase` struct and all methods. Replace with:

```go
import "flowforge/internal/tenant/mocks"

// In setup:
tenantUC := mocks.NewMockTenantUseCase(t)
tenantUC.EXPECT().GetBySlug(mock.Anything, "default-tenant").Return(tnt, nil)
```

Replace `MockUserRepository` references with mockery-generated mocks:

```go
import authmocks "flowforge/internal/auth/mocks"

userRepo := authmocks.NewMockUserRepository(t)
userRepo.EXPECT().FindByEmail(mock.Anything, tnt.ID, "admin@flowforge.local").Return(user, nil)
```

---

#### [MODIFY] [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler_test.go)

**Delete** `MockTenantRepository` struct. Replace with mockery mock:

```go
import tenantmocks "flowforge/internal/tenant/mocks"

tenantRepo := tenantmocks.NewMockTenantRepository(t)
```

---

#### [MODIFY] [token_blacklist_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/token_blacklist_test.go)

**Delete** `mockBlacklistStore`. Replace with mockery mock for `TokenBlacklist`:

```go
import authmocks "flowforge/internal/auth/mocks"

bl := authmocks.NewMockTokenBlacklist(t)
bl.EXPECT().IsRevoked(mock.Anything, "fresh-jti-123").Return(false, nil)
```

---

## Verification Plan

### Automated Tests

```bash
# 1. Generate mocks
make mocks

# 2. Compile check
make build

# 3. Static analysis
make vet

# 4. Full test suite with race detection
make test
```

---

## Fix Order

| Step | Task | Files |
|------|------|-------|
| 1 | Fix `repository.go` bugs (B-1, B-5, B-6) | `internal/platform/postgres/repository.go` |
| 2 | Fix `repository_test.go` (B-7) | `internal/platform/postgres/repository_test.go` |
| 3 | Fix `auth/repository.go` (B-2, B-3, B-4) | `internal/auth/repository.go` |
| 4 | Fix `user_handler.go` (B-8) | `internal/auth/user_handler.go` |
| 5 | Create `.mockery.yaml` + `Makefile` | root |
| 6 | Install mockery + generate mocks | `make install-mockery && make mocks` |
| 7 | Migrate `usecase_test.go` to mockery mocks | `internal/auth/usecase_test.go` |
| 8 | Migrate `handler_test.go` to mockery mocks | `internal/auth/handler_test.go` |
| 9 | Migrate `repository_test.go` (auth) to mockery | `internal/auth/repository_test.go` |
| 10 | Migrate `token_blacklist_test.go` to mockery | `internal/auth/token_blacklist_test.go` |
| 11 | Run `make test` — all green | — |

# Phase 3 — Workflow Domain Foundations (Implementation Blueprint **v2**)

**Planner:** Claude (Architect) · **Executor:** Gemini Flash 3.6
**Source docs:** `backlog.md` §Phase 3 · `api-1.md` §5.4–5.7, §7 · `api-2.md` §9.4–9.6 · `database_design.md` §7.3–7.6, §11 · `AGENTS.md`
**Predecessor:** Phase 2 landed as `4219c0f` (0 open findings)

> **v2 changelog.** v1 was reviewed against the real codebase and had 14 gaps (G-1…G-14). v2 closes G-1…G-9 and folds G-10…G-14 in as inline rules. Specifically: the node-identity mismatch is resolved (§3.2), all 13 DTOs are now fully defined (§3.5), version-number allocation is specified (§3.6), the assertion library is corrected to **testify** (v1 wrongly said `matryer/is` — zero files use it), response DTOs are mapped to the `api-1.md` envelope (§3.7), and audit logging is brought **into** scope (§3.8).

---

## 1. Task Summary

**Goal.** Workflow ownership and versioning: metadata CRUD, a mutable draft graph, immutable publish snapshots with monotonic version numbers, a current-version pointer, and rollback — all tenant-scoped, optimistically locked, publish/rollback atomic.

**The schema already exists.** `migrations/000001_init_schema.up.sql:29-112` defines `workflows`, `workflow_versions`, `workflow_nodes`, `workflow_edges` with composite `(tenant_id, id)` FKs and the circular `fk_workflows_current_version`. `audit_logs` exists at `:181-196`. **No new migration is required** (one optional hardening index is offered in §3.6 — clearly marked).

**Deferred, with owning phase:**

| Deferred | Owner |
|---|---|
| `workflow.created` / `.published` / `.rolledBack` events | Phase 7 (needs Redis Pub/Sub) |
| `Idempotency-Key` handling | Phase 6 (`api-3.md` makes it required for runs) |
| Execution / `workflow_runs` | Phase 5–6 |
| `expr-lang` validation of node `config` | Phase 4 |

---

## 2. Resolved Decisions

| # | Decision | Rationale |
|---|---|---|
| **D-1** | Draft lives as a persistent `workflow_versions` row with `status='draft'`; new endpoint `PUT /api/v1/workflows/{workflowId}/draft` | The spec's publish body is `{rowVersion}` only, so the graph must pre-exist server-side, but no spec endpoint writes nodes/edges. Zero schema change; published rows immutable by construction. |
| **D-2** | New `internal/platform/httpx` envelope per `api-1.md` §5.4, used by Phase 3 **and** retrofitted onto `internal/auth` in this phase | `internal/auth` currently writes bare payloads ([handler.go:200-204](../../internal/auth/handler.go#L200-L204)) — already off-spec. Retrofitting now avoids two API dialects. |
| **D-3** | `current_version_number = 0` set explicitly on insert | `migration:36` defaults to 1, `api-2.md:916` says 0. Zero is correct — no version is current yet. Nothing CHECKs the column. |
| **D-4** | **Repository tested via pure SQL-builder functions**, not a DB mock | See §5.4. `pgxmock` was considered and rejected: `NewBaseRepository` takes `*pgxpool.Pool` concretely ([repository.go:38](../../internal/platform/postgres/repository.go#L38)), so adopting it forces a refactor of Phase 1/2 platform code — out of scope here. |
| **D-5** | Audit logging is **in scope** | `api-2.md` lists `audit_logs` writes on 5 endpoints, the table exists, and for publish/rollback the entry must share the transaction to be trustworthy. |

---

## 3. Architecture

New package `internal/workflow/` (flat, mirroring `internal/auth/`). Domain types in `internal/domain/`.

### 3.1 Domain persistence structs — `internal/domain/workflow.go`

Tags follow [user.go](../../internal/domain/user.go). `BaseRepository.structToMap` reads `db`; `pgx.RowToAddrOfStructByName` needs a field per selected column. **Nullable columns MUST be pointers** — otherwise scanning fails at runtime, not compile time.

```go
type Workflow struct {
    ID                   uuid.UUID  `db:"id"                     json:"id"`
    TenantID             uuid.UUID  `db:"tenant_id"              json:"tenantId"`
    Name                 string     `db:"name"                   json:"name"`
    Description          string     `db:"description"            json:"description"`
    Status               string     `db:"status"                 json:"status"`
    CurrentVersionNumber int        `db:"current_version_number" json:"currentVersionNumber"`
    CurrentVersionID     *uuid.UUID `db:"current_version_id"     json:"currentVersionId"` // NULLABLE
    RowVersion           int        `db:"row_version"            json:"rowVersion"`
    CreatedAt            time.Time  `db:"created_at"             json:"createdAt"`
    UpdatedAt            time.Time  `db:"updated_at"             json:"updatedAt"`
}

type WorkflowVersion struct {
    ID            uuid.UUID       `db:"id"             json:"id"`
    TenantID      uuid.UUID       `db:"tenant_id"      json:"tenantId"`
    WorkflowID    uuid.UUID       `db:"workflow_id"    json:"workflowId"`
    VersionNumber int             `db:"version_number" json:"versionNumber"`
    Status        string          `db:"status"         json:"status"`
    GraphSnapshot json.RawMessage `db:"graph_snapshot" json:"graphSnapshot"`
    Metadata      json.RawMessage `db:"metadata"       json:"metadata"`
    Checksum      string          `db:"checksum"       json:"checksum"`
    CreatedBy     *uuid.UUID      `db:"created_by"     json:"createdBy"`   // NULLABLE
    PublishedAt   *time.Time      `db:"published_at"   json:"publishedAt"` // NULLABLE
    CreatedAt     time.Time       `db:"created_at"     json:"createdAt"`
}

type WorkflowNode struct {
    ID                uuid.UUID       `db:"id"                  json:"id"`
    TenantID          uuid.UUID       `db:"tenant_id"           json:"-"`
    WorkflowVersionID uuid.UUID       `db:"workflow_version_id" json:"-"`
    NodeKey           string          `db:"node_key"            json:"nodeKey"`
    NodeType          string          `db:"node_type"           json:"nodeType"`
    Config            json.RawMessage `db:"config"              json:"config"`
    PositionX         int             `db:"position_x"          json:"positionX"`
    PositionY         int             `db:"position_y"          json:"positionY"`
    CreatedAt         time.Time       `db:"created_at"          json:"-"`
}

type WorkflowEdge struct {
    ID                uuid.UUID `db:"id"                  json:"id"`
    TenantID          uuid.UUID `db:"tenant_id"           json:"-"`
    WorkflowVersionID uuid.UUID `db:"workflow_version_id" json:"-"`
    FromNodeID        uuid.UUID `db:"from_node_id"        json:"-"`
    ToNodeID          uuid.UUID `db:"to_node_id"          json:"-"`
    CreatedAt         time.Time `db:"created_at"          json:"-"`
}
```

Constants — never scatter the literals; the CHECK constraints are the contract:

```go
const (
    WorkflowStatusDraft, WorkflowStatusPublished = "draft", "published"
    WorkflowStatusArchived, WorkflowStatusDisabled = "archived", "disabled"

    VersionStatusDraft, VersionStatusPublished, VersionStatusArchived = "draft", "published", "archived"

    NodeTypeHTTP, NodeTypeDelay      = "HTTP", "DELAY"
    NodeTypeCondition, NodeTypeTransform = "CONDITION", "TRANSFORM"
)

func IsValidNodeType(t string) bool // HTTP | DELAY | CONDITION | TRANSFORM
```

### 3.2 Graph representation — **closes G-1, the v1 blocking defect**

There are **two** graph representations and v1 conflated them:

- **Wire/logical form** — nodes identified by `nodeKey` (a string), edges by `from`/`to` **keys**. This is what the client sends, what validation reasons about, and what the engine executes on. Clients cannot send node UUIDs because on a fresh draft the nodes do not exist yet.
- **Persisted form** — `workflow_nodes.id` UUIDs, with `workflow_edges.from_node_id`/`to_node_id` as UUID FKs ([migration:101-108](../../migrations/000001_init_schema.up.sql#L101-L108)).

**Rule: the engine and use case speak keys. Only the repository knows UUIDs.**

```go
// internal/domain/graph.go — the wire/logical form.
type NodeInput struct {
    NodeKey   string          `json:"nodeKey"`
    NodeType  string          `json:"nodeType"`
    Config    json.RawMessage `json:"config"`
    PositionX int             `json:"positionX"`
    PositionY int             `json:"positionY"`
}

type EdgeInput struct {
    From string `json:"from"` // node_key, NOT uuid
    To   string `json:"to"`   // node_key, NOT uuid
}

type Graph struct {
    Nodes []NodeInput `json:"nodes"`
    Edges []EdgeInput `json:"edges"`
}
```

Two translation helpers live in `internal/domain/graph.go` and are pure:

```go
// ToPersisted assigns fresh UUIDs to nodes and resolves edge keys to those UUIDs.
// Returns ErrEdgeNodeNotFound if an edge names a key with no node.
func (g Graph) ToPersisted(tenantID, versionID uuid.UUID, now time.Time) ([]WorkflowNode, []WorkflowEdge, error)

// FromPersisted rebuilds the key-based Graph from stored rows (UUID -> key).
// Nodes are sorted by NodeKey, edges by (From, To), so output is canonical.
func FromPersisted(nodes []WorkflowNode, edges []WorkflowEdge) Graph
```

`FromPersisted` returning **canonical order** is what makes the checksum stable (T-18) and the publish snapshot reproducible. Do not skip the sort.

### 3.3 Engine — `internal/engine/dag.go` (PURE, new package)

Operates on the **key-based** form only:

```go
package engine

// ValidateDAG checks: non-empty; node keys unique and non-empty; node types valid;
// every edge endpoint resolves to a node key; no self-loops; no cycles.
func ValidateDAG(g domain.Graph) error

// TopologicalOrder returns node keys in deterministic execution order.
// Kahn's algorithm; the ready-set is drained in ascending node_key order so
// output is byte-identical across runs.
func TopologicalOrder(g domain.Graph) ([]string, error)
```

Error mapping inside the engine:

| Condition | Returns |
|---|---|
| no nodes | `workflow.ErrEmptyGraph`? **No** — engine must not import `workflow`. Return `domain.ErrInvalidDAG` wrapped: `fmt.Errorf("%w: graph has no nodes", domain.ErrInvalidDAG)` |
| duplicate / empty node key | `%w: duplicate node key %q` on `domain.ErrInvalidDAG` |
| unknown node type | `%w: unknown node type %q` on `domain.ErrInvalidDAG` |
| edge endpoint unresolved | `%w: edge references unknown node %q` on `domain.ErrInvalidDAG` |
| self-loop or cycle | `domain.ErrCycleDetected` (wrapped with the participating keys) |

**Import discipline:** `internal/engine` may import **only** stdlib + `flowforge/internal/domain`. Never `internal/workflow` — that would invert the dependency. This is why every engine error is built on the two `domain.*` sentinels that already exist at [errors.go:13-14](../../internal/domain/errors.go#L13-L14).

Deterministic tie-breaking is mandatory: `backlog.md` Phase 4 requires deterministic execution order, and a map-ranged Kahn is not deterministic.

### 3.4 Sentinel errors — `internal/workflow/errors.go`

```go
var (
    ErrWorkflowNotFound      = errors.New("workflow not found")
    ErrWorkflowAlreadyExists = errors.New("workflow name already exists in tenant")
    ErrWorkflowArchived      = errors.New("workflow is archived")
    ErrVersionNotFound       = errors.New("workflow version not found")
    ErrVersionConflict       = errors.New("stale row version")
    ErrVersionImmutable      = errors.New("published version is immutable")
    ErrDraftMissing          = errors.New("workflow has no draft version")
    ErrNameRequired          = errors.New("workflow name is required")
    ErrInvalidSortField      = errors.New("invalid sort field")
)
```

Graph-shape failures are **not** re-declared here — they arrive from the engine as `domain.ErrInvalidDAG` / `domain.ErrCycleDetected` and are mapped straight to HTTP in §3.9.

### 3.5 DTOs — **closes G-2; every type v1 referenced is now defined**

`internal/workflow/dto.go`:

```go
// ---------- commands / queries (use-case inputs) ----------

type CreateWorkflowCommand struct {
    TenantID    uuid.UUID
    ActorID     uuid.UUID
    Name        string
    Description string
}

type UpdateWorkflowCommand struct {
    TenantID    uuid.UUID
    ActorID     uuid.UUID
    WorkflowID  uuid.UUID
    Name        *string // nil = unchanged
    Description *string // nil = unchanged
    RowVersion  int     // required
}

type ListWorkflowsQuery struct {
    TenantID  uuid.UUID
    Page      int    // default 1
    PageSize  int    // default 20, max 100
    Status    string // "" = all except archived
    Search    string // ILIKE over name
    SortBy    string // allowlist: name|created_at|updated_at|status ; default updated_at
    SortOrder string // asc|desc ; default desc
}

type SaveDraftCommand struct {
    TenantID   uuid.UUID
    ActorID    uuid.UUID
    WorkflowID uuid.UUID
    Graph      domain.Graph
    RowVersion int // required
}

type PublishCommand struct {
    TenantID   uuid.UUID
    ActorID    uuid.UUID
    WorkflowID uuid.UUID
    RowVersion int // required
}

type RollbackCommand struct {
    TenantID   uuid.UUID
    ActorID    uuid.UUID
    WorkflowID uuid.UUID
    VersionID  uuid.UUID
    RowVersion int // required
}

// ---------- results (use-case outputs) ----------

type PaginatedWorkflows struct {
    Items      []*domain.Workflow
    TotalItems int64
    Page       int
    PageSize   int
}

type VersionDetail struct {
    Version *domain.WorkflowVersion
    Graph   domain.Graph // for a draft, assembled from nodes/edges; for published, decoded from graph_snapshot
}

type PublishResult struct {
    WorkflowID    uuid.UUID
    VersionID     uuid.UUID
    VersionNumber int
    Status        string
    PublishedAt   time.Time
}

type RollbackResult struct {
    WorkflowID    uuid.UUID
    VersionID     uuid.UUID
    VersionNumber int
    Status        string
    RolledBackAt  time.Time
}
```

**Repository filter type** (`repository.go`) — deliberately distinct from `ListWorkflowsQuery`: the use case validates and normalises, the repo receives only already-safe values.

```go
type ListWorkflowsFilter struct {
    TenantID       uuid.UUID
    Page, PageSize int
    Status         string // "" = no status predicate
    Search         string // already %/_-escaped by the repo
    OrderByColumn  string // pre-validated against the allowlist — NEVER raw user input
    OrderAsc       bool
    ExcludeStatus  string // "archived" unless the caller asked for it
}
```

### 3.6 Version lifecycle — **closes G-3**

The invariant, stated once: **every workflow owns exactly one `workflow_versions` row with `status='draft'`, and it always holds the highest `version_number`.**

| Operation | Version effect |
|---|---|
| `CreateWorkflow` | In one tx: insert `workflows` (`current_version_number=0`, `current_version_id=NULL`, `row_version=1`, `status='draft'`) **and** insert draft `v1` with an empty graph. Creating the draft eagerly keeps `SaveDraftGraph` total — no lazy-create branch, no `ErrDraftMissing` in the happy path. `CHECK (version_number > 0)` is satisfied. |
| `SaveDraftGraph` | Replaces the draft's nodes/edges. **Never** changes `version_number` or creates a row. |
| `PublishVersion` | Draft `v_n` → `status='published'`, `published_at`, `checksum`, `graph_snapshot`. Workflow pointer → `v_n`, `current_version_number=n`, `status='published'`, `row_version+1`. Then insert new draft `v_{n+1}` cloned from `v_n`. |
| `RollbackVersion(target v_k)` | `n = MAX(version_number)+1`. Insert `v_n` as **published**, cloned from `v_k`. Pointer → `v_n`. Then **replace the existing draft's graph** with `v_k`'s graph — do not create a second draft. Target `v_k` is never mutated. |

Rollback replacing (rather than adding) a draft is what preserves the one-draft invariant, and it is semantically right: rolling back means "continue from here".

**Concurrency.** `MAX(version_number)+1` races under concurrent publish, and `uq_workflow_versions_workflow_ver UNIQUE (workflow_id, version_number)` ([migration:60](../../migrations/000001_init_schema.up.sql#L60)) would surface it as a raw 23505 → 500. Prevent it: publish and rollback both begin by locking the aggregate root.

```go
// FindByIDForUpdate issues SELECT ... FOR UPDATE. Publish and rollback MUST use it
// as their first statement, so version allocation and the row_version check are
// serialized on the workflows row (database_design.md §11.2).
FindByIDForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error)
```

Belt and braces: if a 23505 on `uq_workflow_versions_workflow_ver` escapes anyway, map it to `ErrVersionConflict`, never a 500 (**G-13**).

**Optional hardening (the only migration Phase 3 could add — skip unless the user asks):**

```sql
-- 000002_one_draft_per_workflow.up.sql
CREATE UNIQUE INDEX uq_workflow_versions_single_draft
  ON workflow_versions (workflow_id) WHERE status = 'draft';
```

**Archived names (G-10).** `DELETE` archives but keeps the name, and `uq_workflows_tenant_name` is unconditional — so the name stays taken. This is intended (historical runs must remain resolvable); creating a workflow with an archived one's name returns `409 WORKFLOW_ALREADY_EXISTS`. Document it in the handler comment; do not silently rename.

**`Metadata` JSONB (G-14).** Phase 3 never populates it — always write `[]byte("{}")`. Reserved for Phase 10 AI annotations.

### 3.7 HTTP envelope — `internal/platform/httpx` (**closes G-8**)

Exact shapes from `api-1.md` §5.4 / §6.1:

```go
package httpx

type Envelope struct {
    Success bool   `json:"success"`
    Data    any    `json:"data"`
    Meta    any    `json:"meta"`
    Error   *Error `json:"error"`
}

type Error struct {
    Code    string `json:"code"`
    Message string `json:"message"`
    Details any    `json:"details"`
}

type Pagination struct {
    Page       int   `json:"page"`
    PageSize   int   `json:"pageSize"`
    TotalItems int64 `json:"totalItems"`
    TotalPages int   `json:"totalPages"`
    HasNext    bool  `json:"hasNext"`
    HasPrev    bool  `json:"hasPrev"`
}

type List struct {
    Items      any        `json:"items"`
    Pagination Pagination `json:"pagination"`
}

func OK(w http.ResponseWriter, data any)
func Created(w http.ResponseWriter, data any)
func NoContent(w http.ResponseWriter)                      // 204, empty body
func Fail(w http.ResponseWriter, status int, code, msg string)
func FailWithDetails(w http.ResponseWriter, status int, code, msg string, details any)
func NewPagination(page, pageSize int, totalItems int64) Pagination
```

Rules: `Items` must serialise as `[]`, never `null`, on an empty page. `NoContent` writes **no** body. `Fail` never receives `err.Error()` — the message is a fixed human string; the code carries the meaning (`api-1.md:266`).

Error codes are exactly `api-1.md` §7.2 — declare them as constants and use no string literals at call sites:
`AUTH_UNAUTHORIZED`, `AUTH_FORBIDDEN`, `WORKFLOW_NOT_FOUND`, `WORKFLOW_ALREADY_EXISTS`, `WORKFLOW_NAME_REQUIRED`, `WORKFLOW_VERSION_NOT_FOUND`, `WORKFLOW_VERSION_CONFLICT`, `WORKFLOW_VERSION_IMMUTABLE`, `WORKFLOW_INVALID_DAG`, `WORKFLOW_CYCLE_DETECTED`, `WORKFLOW_NODE_INVALID`, `WORKFLOW_EDGE_INVALID`, `VALIDATION_ERROR`, `INVALID_REQUEST_BODY`, `INVALID_PATH_PARAMETER`, `INVALID_QUERY_PARAMETER`, `INTERNAL_SERVER_ERROR`.

**Auth retrofit (D-2).** Rewrite `respondJSON`/`respondJSONError` in [handler.go:200-204](../../internal/auth/handler.go#L200-L204) and `respondJSONError` in [middleware.go:144](../../internal/auth/middleware.go#L144) to delegate to `httpx`, then fix the affected assertions in `handler_test.go`, `user_handler_test.go`, `middleware_test.go`, and `main_test.go`. Payload assertions move from `body["id"]` to `body["data"]["id"]`; error assertions to `body["error"]["code"]`. **Do this in one commit** so the suite is never half-converted.

**Response field mapping (G-5).** Handlers must not marshal domain structs directly where `api-2.md` specifies a subset:

| Endpoint | `data` shape |
|---|---|
| `GET /workflows` | `httpx.List` of `{id, name, description, status, currentVersionNumber, updatedAt}` (api-2.md:747-764) |
| `GET /workflows/{id}` | `{id, name, description, status, currentVersionNumber, currentVersionId, rowVersion, createdAt, updatedAt}` (api-2.md:814-831) |
| `POST /workflows` | adds `tenantId`; `currentVersionNumber: 0` (api-2.md:908-923) |
| `PATCH /workflows/{id}` | `{id, name, description, rowVersion, updatedAt}` (api-2.md:1000-1013) |
| `GET .../versions` | `{items: [{id, workflowId, versionNumber, status, publishedAt, createdAt}]}` — **no** `pagination` object here; the spec lists items only (api-2.md:1124-1142) |
| `GET .../versions/{id}` | `{id, workflowId, versionNumber, status, graphSnapshot:{nodes,edges}, metadata, checksum, createdAt, publishedAt}` (api-2.md:1188-1207) |
| `POST .../publish` | `{workflowId, versionId, versionNumber, status, publishedAt}` |
| `POST .../rollback` | `{workflowId, versionId, versionNumber, status, rolledBackAt}` |
| `PUT .../draft` | `{workflowId, versionId, versionNumber, status, rowVersion, updatedAt}` (new endpoint — no spec precedent) |

### 3.8 Audit logging — `internal/workflow/audit.go` (**closes G-6**)

```go
type AuditEntry struct {
    ID          uuid.UUID       `db:"id"`
    TenantID    uuid.UUID       `db:"tenant_id"`
    ActorUserID *uuid.UUID      `db:"actor_user_id"` // NULLABLE
    Action      string          `db:"action"`
    EntityType  string          `db:"entity_type"`
    EntityID    *uuid.UUID      `db:"entity_id"`
    Metadata    json.RawMessage `db:"metadata"`
    CreatedAt   time.Time       `db:"created_at"`
}

type AuditRepository interface {
    Record(ctx context.Context, e AuditEntry) error
}
```

Actions: `workflow.created`, `workflow.updated`, `workflow.archived`, `workflow.draft_saved`, `workflow.published`, `workflow.rolled_back`. `entity_type` is always `"workflow"`.

**Placement matters.** For `publish` and `rollback` the `Record` call goes **inside** the same `ExecuteInTx` — an audit trail that survives a rolled-back transaction is a lie. Same-tx also means an audit failure aborts the operation, which is the correct trade for these two. Uses `BaseRepository[AuditEntry].Create`; no bespoke SQL.

### 3.9 Repository — `internal/workflow/repository.go`

```go
type WorkflowRepository interface {
    Create(ctx context.Context, wf *domain.Workflow) error
    FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error)
    FindByIDForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) // §3.6
    List(ctx context.Context, f ListWorkflowsFilter) ([]*domain.Workflow, int64, error)
    UpdateMetadata(ctx context.Context, wf *domain.Workflow, expectedRowVersion int) error
    UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status string, expectedRowVersion int) error
    SetCurrentVersion(ctx context.Context, tenantID, workflowID, versionID uuid.UUID, versionNumber, expectedRowVersion int) error
}

type VersionRepository interface {
    CreateVersion(ctx context.Context, v *domain.WorkflowVersion) error
    FindVersionByID(ctx context.Context, tenantID, workflowID, versionID uuid.UUID) (*domain.WorkflowVersion, error)
    FindDraftVersion(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.WorkflowVersion, error)
    ListVersions(ctx context.Context, tenantID, workflowID uuid.UUID) ([]*domain.WorkflowVersion, error)
    MaxVersionNumber(ctx context.Context, tenantID, workflowID uuid.UUID) (int, error)
    MarkPublished(ctx context.Context, tenantID, versionID uuid.UUID, checksum string, snapshot []byte, publishedAt time.Time) error
    ReplaceGraph(ctx context.Context, tenantID, versionID uuid.UUID, nodes []domain.WorkflowNode, edges []domain.WorkflowEdge) error
    LoadGraph(ctx context.Context, tenantID, versionID uuid.UUID) ([]domain.WorkflowNode, []domain.WorkflowEdge, error)
}
```

**Reuse `postgres.BaseRepository[T]`** for `Create`, `FindByFilter`, `ListVersions` — as [auth/repository.go:36-40](../../internal/auth/repository.go#L36-L40) does. Map at the repo boundary: `domain.ErrNotFound` → `ErrWorkflowNotFound`/`ErrVersionNotFound`, `domain.ErrConflict` → `ErrWorkflowAlreadyExists`.

**Five hand-written queries** — extract each one's SQL construction into a **pure builder function** so it is directly testable (D-4):

```go
func buildUpdateMetadataSQL(wf *domain.Workflow, expected int) (string, []any, error)
func buildSetCurrentVersionSQL(tenantID, wfID, verID uuid.UUID, num, expected int) (string, []any, error)
func buildUpdateStatusSQL(tenantID, id uuid.UUID, status string, expected int) (string, []any, error)
func buildListWorkflowsSQL(f ListWorkflowsFilter) (items string, itemArgs []any, count string, countArgs []any, err error)
func buildMaxVersionSQL(tenantID, wfID uuid.UUID) (string, []any, error)
```

1. **Optimistic locking.** `BaseRepository.Update` ([repository.go:210](../../internal/platform/postgres/repository.go#L210)) has no version predicate. Every mutating workflow write is:
   ```sql
   UPDATE workflows SET ..., row_version = row_version + 1, updated_at = NOW()
   WHERE tenant_id = $1 AND id = $2 AND row_version = $3
   ```
   `RowsAffected() == 0` is **ambiguous** — re-read the row: absent → `ErrWorkflowNotFound`, present → `ErrVersionConflict`. Returning 404 for a stale write is wrong.
2. **`List` with `search`.** `Paginate` only supports `sq.Eq`; `search` needs `ILIKE`. Escape `\`, `%`, `_` in the user string **before** wrapping in `%…%`. `OrderByColumn` comes pre-validated from the use case — never interpolate raw input, and do not lean on `sanitizeOrderBy` ([repository.go:270-276](../../internal/platform/postgres/repository.go#L270-L276)), which silently swallows bad input into `created_at DESC`. Exclude `archived` unless `Status` explicitly asks for it (api-2.md:731).
3. **`ReplaceGraph`.** Delete edges, delete nodes, insert nodes, insert edges — **in that order**, in the caller's transaction. `fk_workflow_edges_from_node` references `(tenant_id, workflow_version_id, id)`, so edges must go after nodes and be deleted before them.
4. **`FindByIDForUpdate`.** `SELECT * FROM workflows WHERE tenant_id=$1 AND id=$2 FOR UPDATE`.
5. **`MaxVersionNumber`.** `SELECT COALESCE(MAX(version_number),0) FROM workflow_versions WHERE tenant_id=$1 AND workflow_id=$2`.

**Every query filters `tenant_id`. No exceptions.**

### 3.10 UseCase — `internal/workflow/usecase.go` (ZERO `net/http`)

```go
type WorkflowUseCase interface {
    CreateWorkflow(ctx context.Context, cmd CreateWorkflowCommand) (*domain.Workflow, error)
    ListWorkflows(ctx context.Context, q ListWorkflowsQuery) (*PaginatedWorkflows, error)
    GetWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.Workflow, error)
    UpdateWorkflow(ctx context.Context, cmd UpdateWorkflowCommand) (*domain.Workflow, error)
    ArchiveWorkflow(ctx context.Context, cmd ArchiveWorkflowCommand) error
    SaveDraftGraph(ctx context.Context, cmd SaveDraftCommand) (*domain.WorkflowVersion, error)
    ListVersions(ctx context.Context, tenantID, workflowID uuid.UUID) ([]*domain.WorkflowVersion, error)
    GetVersion(ctx context.Context, tenantID, workflowID, versionID uuid.UUID) (*VersionDetail, error)
    PublishVersion(ctx context.Context, cmd PublishCommand) (*PublishResult, error)
    RollbackVersion(ctx context.Context, cmd RollbackCommand) (*RollbackResult, error)
}

// ArchiveWorkflowCommand mirrors the others: {TenantID, ActorID, WorkflowID, RowVersion}.

// Consumer-side interface — postgres.UnitOfWork satisfies it structurally
// (verified: unit_of_work.go:14). Deliberately NOT imported from internal/auth,
// which would couple two feature packages.
type TxRunner interface {
    ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// NewWorkflowUseCase builds a WorkflowUseCase bound to a transaction boundary.
// txRunner must not be nil — pass NewPassthroughTxRunner() to opt out explicitly.
// (Phase 2 Y-1 contract: no nil fallback, so a missing runner panics loudly
// instead of silently disabling atomicity.)
func NewWorkflowUseCase(wf WorkflowRepository, ver VersionRepository, audit AuditRepository, tx TxRunner) WorkflowUseCase
```

**Validation, in the use case (not the handler, not the repo):** `name` non-empty after trim, ≤255 → else `ErrNameRequired`; `SortBy` ∈ {`name`,`created_at`,`updated_at`,`status`} → else `ErrInvalidSortField`; `PageSize` clamped to 1…100 (default 20); node types valid; `RowVersion` ≥ 0.

**`PublishVersion` — the critical path.** All of it inside **one** `ExecuteInTx`:

1. `FindByIDForUpdate` → not found → `ErrWorkflowNotFound`.
2. `wf.RowVersion != cmd.RowVersion` → `ErrVersionConflict`. **Before any write.**
3. `wf.Status == archived` → `ErrWorkflowArchived`.
4. `FindDraftVersion` → missing → `ErrDraftMissing`. `LoadGraph` → `domain.FromPersisted` → canonical `Graph`.
5. `engine.ValidateDAG(graph)` — propagate `domain.ErrCycleDetected` / `domain.ErrInvalidDAG` **unchanged** (`errors.Is` must still match at the handler).
6. `snapshot, _ := json.Marshal(graph)`; `checksum = hex(sha256(snapshot))`. Canonical ordering comes from `FromPersisted`, so an identical graph always yields an identical checksum.
7. `MarkPublished(draft.ID, checksum, snapshot, now)`.
8. `SetCurrentVersion(...expectedRowVersion=cmd.RowVersion)` → 0 rows → `ErrVersionConflict`.
9. `UpdateStatus(published, expected = cmd.RowVersion+1)` — note the bump from step 8. Alternatively fold status into `SetCurrentVersion`'s single UPDATE; **preferred**, one statement, no second version dance.
10. `CreateVersion(v_{n+1}, draft)` + `ReplaceGraph` with the same nodes/edges re-UUID'd via `graph.ToPersisted`.
11. `audit.Record(workflow.published)`.

**Immutability is application-layer** (`database_design.md:415`) — no DB trigger. So `SaveDraftGraph` must refuse any version with `status != 'draft'` → `ErrVersionImmutable`, and that refusal needs its own test (T-16).

**`GetVersion` (G-9).** A draft's `graph_snapshot` is `'{}'` until publish. So: if `status == draft` → assemble via `LoadGraph` + `FromPersisted`; else → decode the stored `graph_snapshot`. Never return `{}` for a draft that has nodes.

### 3.11 Delivery — `internal/workflow/handler.go`

Decode → `AuthUserFromContext` → delegate → map. Mirror [user_handler.go](../../internal/auth/user_handler.go): `http.MaxBytesReader(w, r.Body, 1<<20)` on every body route, `uuid.Parse` on every path value.

| Error | Status | Code |
|---|---|---|
| `ErrWorkflowNotFound` | 404 | `WORKFLOW_NOT_FOUND` |
| `ErrVersionNotFound`, `ErrDraftMissing` | 404 | `WORKFLOW_VERSION_NOT_FOUND` |
| `ErrWorkflowAlreadyExists` | 409 | `WORKFLOW_ALREADY_EXISTS` |
| `ErrVersionConflict` | 409 | `WORKFLOW_VERSION_CONFLICT` |
| `ErrVersionImmutable`, `ErrWorkflowArchived` | 409 | `WORKFLOW_VERSION_IMMUTABLE` |
| `domain.ErrCycleDetected` | 409 | `WORKFLOW_CYCLE_DETECTED` |
| `domain.ErrInvalidDAG` | 409 | `WORKFLOW_INVALID_DAG` |
| `ErrNameRequired` | 422 | `WORKFLOW_NAME_REQUIRED` |
| `ErrInvalidSortField` | 422 | `INVALID_QUERY_PARAMETER` |
| malformed JSON | 400 | `INVALID_REQUEST_BODY` |
| bad UUID in path | 400 | `INVALID_PATH_PARAMETER` |
| default | 500 | `INTERNAL_SERVER_ERROR` — never leak `err.Error()` |

Check `domain.ErrCycleDetected` **before** `domain.ErrInvalidDAG` if the engine ever wraps both. `DELETE` returns **204 empty** (api-2.md:1076) — differs from Phase 2's `DeleteUser`, which returns 200 + body.

### 3.12 Wiring — `cmd/api/main.go`

Reuse the `uow` already built at [main.go:222](../../cmd/api/main.go#L222); do not construct a second one.

```go
workflowRepo := workflow.NewWorkflowRepository(dbPool)
versionRepo  := workflow.NewVersionRepository(dbPool)
auditRepo    := workflow.NewAuditRepository(dbPool)
workflowUC   := workflow.NewWorkflowUseCase(workflowRepo, versionRepo, auditRepo, uow)
workflowHandler = workflow.NewWorkflowHandler(workflowUC)
```

Routes (viewer reads, editor+admin writes, per api-2.md):

```go
mux.Handle("GET    /api/v1/workflows",                                            authMW.Authenticate(auth.RequireRole("admin","editor","viewer")(http.HandlerFunc(h.ListWorkflows))))
mux.Handle("POST   /api/v1/workflows",                                            authMW.Authenticate(auth.RequireRole("admin","editor")(http.HandlerFunc(h.CreateWorkflow))))
mux.Handle("GET    /api/v1/workflows/{workflowId}",                               authMW.Authenticate(auth.RequireRole("admin","editor","viewer")(http.HandlerFunc(h.GetWorkflow))))
mux.Handle("PATCH  /api/v1/workflows/{workflowId}",                               authMW.Authenticate(auth.RequireRole("admin","editor")(http.HandlerFunc(h.UpdateWorkflow))))
mux.Handle("DELETE /api/v1/workflows/{workflowId}",                               authMW.Authenticate(auth.RequireRole("admin","editor")(http.HandlerFunc(h.DeleteWorkflow))))
mux.Handle("PUT    /api/v1/workflows/{workflowId}/draft",                          authMW.Authenticate(auth.RequireRole("admin","editor")(http.HandlerFunc(h.SaveDraft))))
mux.Handle("GET    /api/v1/workflows/{workflowId}/versions",                       authMW.Authenticate(auth.RequireRole("admin","editor","viewer")(http.HandlerFunc(h.ListVersions))))
mux.Handle("GET    /api/v1/workflows/{workflowId}/versions/{versionId}",           authMW.Authenticate(auth.RequireRole("admin","editor","viewer")(http.HandlerFunc(h.GetVersion))))
mux.Handle("POST   /api/v1/workflows/{workflowId}/versions/publish",               authMW.Authenticate(auth.RequireRole("admin","editor")(http.HandlerFunc(h.Publish))))
mux.Handle("POST   /api/v1/workflows/{workflowId}/versions/{versionId}/rollback",  authMW.Authenticate(auth.RequireRole("admin","editor")(http.HandlerFunc(h.Rollback))))
```

Go 1.26's `ServeMux` matches `/versions/publish` against `/versions/{versionId}` — the literal wins over the wildcard, so registration order is irrelevant, **but add a test** proving `POST .../versions/publish` does not land in the rollback handler.

### 3.13 Mocks — `.mockery.yaml`

```yaml
  flowforge/internal/workflow:
    config:
      dir: "internal/workflow/mocks"
      outpkg: "mocks"
    interfaces:
      WorkflowRepository:
      VersionRepository:
      AuditRepository:
      WorkflowUseCase:
```

Then `make mocks`.

---

## 4. Boundary Checklist

- [ ] `internal/engine/` imports only stdlib + `flowforge/internal/domain`. Enforced by `purity_test.go` (**T-26**).
- [ ] `internal/workflow/usecase.go` has zero `net/http` imports.
- [ ] `internal/workflow` never imports `internal/auth` (uses `auth.AuthUserFromContext` in the **handler** only — that one import is expected and confined there).
- [ ] Every repository query filters `tenant_id`.
- [ ] Every mutating workflow write carries a `row_version` predicate and bumps it.
- [ ] Publish and rollback each run in exactly one `ExecuteInTx`, opened with `FindByIDForUpdate`.
- [ ] `NewWorkflowUseCase` rejects nil `TxRunner` (Phase 2 Y-1 contract).
- [ ] Published `workflow_versions` rows are never UPDATEd after `MarkPublished`.
- [ ] `context.Context` first param everywhere; never `context.Background()` in a request path.
- [ ] Node `config` is stored opaquely and never dereferenced — `SSRFValidator` is Phase 5.

---

## 5. TDD Specification

**Assertion library: `github.com/stretchr/testify`** (`assert`/`require`) — already the project standard across every `_test.go`. **Do not introduce `matryer/is`** (v1 said so in error; zero files use it). Mocks via mockery `EXPECT()` builders. Table-driven subtests. Everything runs under `-race`.

### 5.1 `internal/engine/dag_test.go` — pure, no mocks

- **T-1** linear `A→B→C` validates; order `[A B C]`.
- **T-2** diamond `A→B`, `A→C`, `B→D`, `C→D` validates; `A` first, `D` last.
- **T-3** cycle `A→B→C→A` → `errors.Is(err, domain.ErrCycleDetected)`.
- **T-4** self-loop `A→A` → `ErrCycleDetected`. The DB CHECK also blocks it; the engine must not rely on that.
- **T-5** edge naming an unknown key → `errors.Is(err, domain.ErrInvalidDAG)`.
- **T-6** duplicate node key → `ErrInvalidDAG`. **T-6b** empty node key → `ErrInvalidDAG`.
- **T-7** empty graph → `ErrInvalidDAG`.
- **T-8 determinism** — wide fan-out graph, `TopologicalOrder` 100×, byte-identical every time. **This is the test that catches map-iteration order; it must exist.**
- **T-9** two disconnected chains both validate and both appear.
- **T-9b** unknown node type → `ErrInvalidDAG`.

### 5.2 `internal/domain/graph_test.go`

- **T-27** `ToPersisted` assigns distinct UUIDs and resolves every edge; an edge naming a missing key errors.
- **T-28** `FromPersisted(ToPersisted(g))` round-trips to a `Graph` equal to `g` in canonical order.
- **T-29** `FromPersisted` output is canonical — feed the same rows shuffled, get identical output.

### 5.3 `internal/workflow/usecase_test.go` — mocked repos

- **T-10** create: happy path; name collision → `ErrWorkflowAlreadyExists`; blank name → `ErrNameRequired`; asserts `current_version_number == 0` (D-3) **and** that draft `v1` was created in the same call (§3.6).
- **T-11** stale row version on `UpdateWorkflow` → `ErrVersionConflict`, and **no** `UpdateMetadata` call reached the mock.
- **T-12** tenant isolation: a workflow owned by tenant B fetched as tenant A → `ErrWorkflowNotFound`. Never 403; never leak existence.
- **T-13** publish happy path — recording mock pins the **order**: `FindByIDForUpdate` → `ValidateDAG` → `MarkPublished` → `SetCurrentVersion` → next draft → `audit.Record`. Assert the sequence, not just membership. *(X-3 lesson: v1's ordering test held only transitively.)*
- **T-14** publish with a cyclic draft → `errors.Is(err, domain.ErrCycleDetected)` **and** `MarkPublished` never called. The negative assertion is the point.
- **T-15** publish atomicity — `recordingTxRunner` (copy from `user_usecase_test.go`) proves `entered == true`; a mid-tx failure in `SetCurrentVersion` surfaces as `innerErr != nil`.
- **T-16** `SaveDraftGraph` against a `published` version → `ErrVersionImmutable`, no `ReplaceGraph` call.
- **T-17** rollback: clones the target into a new **published** version, repoints the pointer, replaces the draft's graph, and **never updates the target row**. Stale `rowVersion` → `ErrVersionConflict`; target is a draft → `ErrVersionNotFound`.
- **T-18** checksum stability — same graph, nodes supplied shuffled → identical checksum; one config byte changed → different.
- **T-19** cancelled `ctx` into `PublishVersion` → `context.Canceled`, no writes.
- **T-20** `NewWorkflowUseCase(..., nil)` panics on first use (Y-1 mirror).
- **T-30** publish on an archived workflow → `ErrWorkflowArchived`.
- **T-31** `GetVersion` on a draft assembles the graph from nodes/edges, not from the empty `graph_snapshot` (G-9).
- **T-32** `ListWorkflows` rejects `sortBy=password` → `ErrInvalidSortField`; clamps `pageSize=500` → 100.
- **T-33** audit entries are recorded for create/update/archive/publish/rollback, with `publish`'s `Record` **inside** the tx.

### 5.4 `internal/workflow/repository_sql_test.go` — closes G-7 (D-4)

The five builders from §3.9 are pure `(string, []any)` functions, so assert on generated SQL and args with **no database and no new dependency**:

- **T-34** `buildUpdateMetadataSQL` emits `WHERE tenant_id = $x AND id = $y AND row_version = $z` and `SET ... row_version = row_version + 1`. **Fails if any of the three predicates is missing** — the highest-value assertion in the phase.
- **T-35** `buildListWorkflowsSQL` always includes `tenant_id`; injects `ILIKE` only when `Search != ""`; escapes `%`, `_`, `\`; `ORDER BY` only ever contains an allowlisted column; excludes `archived` by default.
- **T-36** `buildSetCurrentVersionSQL` / `buildUpdateStatusSQL` carry the `row_version` predicate.
- **T-37** `buildMaxVersionSQL` uses `COALESCE(MAX(...), 0)` and filters `tenant_id`.
- **T-38** `ReplaceGraph` issues statements in the order delete-edges → delete-nodes → insert-nodes → insert-edges (assert against a recorded statement list via the `DBTX` seam).

**What this deliberately does not cover:** row→struct scanning, real constraint behaviour, and actual transaction semantics. Those are routed to the live smoke test in §8 — stated here so the gap is a decision, not an oversight.

### 5.5 `internal/workflow/handler_test.go` — mocked use case, `httptest`

- **T-21** one subtest per row of the §3.11 table.
- **T-22** RBAC: viewer `POST /workflows` → 403; viewer `GET` → 200.
- **T-23** bad JSON → 400 `INVALID_REQUEST_BODY`; non-UUID path → 400 `INVALID_PATH_PARAMETER`; body > 1 MiB → 413.
- **T-24** `DELETE` → 204 with a genuinely empty body.
- **T-25** force a repo failure; assert the body contains neither the underlying error text nor `pgx`/`SQL`.
- **T-39** every success response is `{success:true, data, meta:null, error:null}`; every failure is `{success:false, data:null, error:{code,message,details}}`.
- **T-40** an empty list serialises `items: []`, not `null`.
- **T-41** routing: `POST .../versions/publish` reaches `Publish`, not `Rollback`.

### 5.6 `internal/platform/httpx/httpx_test.go`

- **T-42** `OK`/`Created`/`Fail` shapes + status codes; `NoContent` writes zero bytes; `NewPagination(2,20,45)` → `{totalPages:3, hasNext:true, hasPrev:true}`; `NewPagination(1,20,0)` → `{totalPages:0, hasNext:false, hasPrev:false}`.

### 5.7 `internal/engine/purity_test.go`

- **T-26** parse `internal/engine`'s imports with `go/parser`; fail on `net/http`, `pgx`, `redis`, `database/sql`, or `internal/workflow`.

### 5.8 Auth retrofit regression

- **T-43** the existing `internal/auth` and `cmd/api` suites pass with assertions moved to `data.*` / `error.code`. **No auth test may be deleted to make the retrofit pass** — reshape assertions only.

---

## 6. Execution Order

All decisions are resolved; nothing blocks. Tests before implementation at every step.

1. `internal/domain/graph.go` + `graph_test.go` (T-27…T-29) — the key/UUID seam everything else depends on.
2. `internal/engine/dag.go` + `dag_test.go` (T-1…T-9b) + `purity_test.go` (T-26). RED → GREEN.
3. `internal/domain/workflow.go` — structs and constants. **Verify every `db` tag against `migrations/000001_init_schema.up.sql:29-112` column by column.** Nullable → pointer.
4. `internal/platform/httpx/` + `httpx_test.go` (T-42).
5. **Auth retrofit (D-2)** — convert `internal/auth` responders to `httpx`, reshape assertions (T-43). Do it now, before new handlers exist, so only one dialect is ever in the tree. `make ci` must be green before moving on.
6. `internal/workflow/errors.go`, `dto.go`.
7. `internal/workflow/repository.go` + `audit.go`, builders extracted; `repository_sql_test.go` (T-34…T-38).
8. `.mockery.yaml` + `make mocks`.
9. `internal/workflow/usecase_test.go` (T-10…T-20, T-30…T-33) RED → `usecase.go` GREEN.
10. `internal/workflow/handler_test.go` (T-21…T-25, T-39…T-41) RED → `handler.go` GREEN.
11. `cmd/api/main.go` wiring + routes; extend `main_test.go` for registration and RBAC.
12. `make ci` — `fmt-check`, `vet`, `build`, `test -race -count=1`. Green, zero races.
13. Append a Phase 3 entry to `.agents/memory/action_history.md`: what was built, decisions as actually taken, anything deferred.

**Reporting rule.** Any runtime figure must come from `-race`. Never quote a non-race number against a `-race` baseline — that was the Phase 2 Y-3/Z-1 error, and the wrong number survived three review rounds.

---

## 7. Definition of Done

- [ ] Workflows created, listed, fetched, updated, archived — all tenant-scoped.
- [ ] Draft graph saved and replaced; the one-draft invariant holds (§3.6).
- [ ] Publish produces an immutable version with a monotonic number, stable checksum, valid DAG; invalid DAGs and cycles rejected **before any write** (T-14).
- [ ] Published versions unmutatable through any path (T-16).
- [ ] Rollback restores without mutating history (T-17).
- [ ] Every stale write rejected (T-11, T-34).
- [ ] Publish and rollback atomic (T-15).
- [ ] Every response uses the `api-1.md` envelope, auth included (T-39, T-43).
- [ ] Audit entries written for all five actions, publish's inside the tx (T-33).
- [ ] `internal/engine` free of infrastructure imports (T-26).
- [ ] `make ci` green, 0 data races.

---

## 8. Carried over from Phase 2 (not Phase 3 work)

1. Z-1/Z-2/Z-3 polish in [phase_2_final_polish.md](phase_2_final_polish.md) — **deliberately skipped** by user decision; shipped as-is in `4219c0f`.
2. **Live Postgres/Redis smoke test — never run.** Phase 3 adds to its checklist: publish under two concurrent clients yields no duplicate `version_number`; `ReplaceGraph` leaves no orphan edges; a stale `rowVersion` returns 409 against a real DB. This is where §5.4's uncovered ground (scanning, constraints, real transactions) gets checked.
3. **PostgreSQL 15+ floor** — `ON DELETE SET NULL (col)` at migration lines 65, 75, 192. Undocumented outside the migration comments; Phase 12 deployment docs must record it.
4. `GET /api/v1/users/{userId}` has no `RequireRole` wrapper while its siblings do ([main.go:140](../../cmd/api/main.go#L140)).

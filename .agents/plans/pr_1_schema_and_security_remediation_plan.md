# PR #1 Remediation Plan — Schema Multi-Tenancy & Security Hardening

This remediation plan addresses the feedback from PR #1 code review ([Comment #4779517549](https://github.com/AlfariziYasir/flowforge/pull/1#pullrequestreview-4779517549)). It hardens multi-tenant database constraints, prevents cross-version graph edge corruption, enforces workflow-version coupling in execution runs, and redacts sensitive credentials from log outputs.

---

## User Review Required

> [!IMPORTANT]
> - **Composite Database Foreign Keys**: Updates `migrations/000001_init_schema.up.sql` to replace single-column foreign keys with composite `(tenant_id, ...)` foreign keys, guaranteeing multi-tenant safety and graph version consistency directly at PostgreSQL engine level.
> - **Credential Redaction**: Adds a URL sanitizer to `internal/platform/logger` scrubbing passwords from Redis and Postgres connection strings prior to structured `slog` output.

---

## Open Questions

- None. All proposed schema constraints match the core design in `database_design.md` and `CONTEXT.md`.

---

## Proposed Changes

### Component 1: Database Migration Schema (`migrations/000001_init_schema.up.sql`)

#### [MODIFY] [000001_init_schema.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql)

1. **`workflows` & `workflow_versions` Coupling**:
   - Add `CONSTRAINT uq_workflows_tenant_id UNIQUE (tenant_id, id)` to `workflows`.
   - Update `workflow_versions` foreign key constraint:
     ```sql
     CONSTRAINT fk_workflow_versions_tenant_workflow
     FOREIGN KEY (tenant_id, workflow_id)
     REFERENCES workflows(tenant_id, id)
     ON DELETE CASCADE
     ```

2. **`workflow_nodes` & `workflow_edges` Version Integrity**:
   - Add `CONSTRAINT uq_workflow_nodes_tenant_version_id UNIQUE (tenant_id, workflow_version_id, id)` to `workflow_nodes`.
   - Update `workflow_edges` foreign key constraints:
     ```sql
     CONSTRAINT fk_workflow_edges_from_node
     FOREIGN KEY (tenant_id, workflow_version_id, from_node_id)
     REFERENCES workflow_nodes(tenant_id, workflow_version_id, id)
     ON DELETE CASCADE,
     CONSTRAINT fk_workflow_edges_to_node
     FOREIGN KEY (tenant_id, workflow_version_id, to_node_id)
     REFERENCES workflow_nodes(tenant_id, workflow_version_id, id)
     ON DELETE CASCADE
     ```

3. **`workflow_versions` & `workflow_runs` Version Coupling**:
   - Add `CONSTRAINT uq_workflow_versions_tenant_wf_id UNIQUE (tenant_id, workflow_id, id)` to `workflow_versions`.
   - Update `workflow_runs` foreign key constraint:
     ```sql
     CONSTRAINT fk_workflow_runs_tenant_wf_ver
     FOREIGN KEY (tenant_id, workflow_id, workflow_version_id)
     REFERENCES workflow_versions(tenant_id, workflow_id, id)
     ON DELETE CASCADE
     ```

---

### Component 2: Migration Down Script (`migrations/000001_init_schema.down.sql`)

#### [MODIFY] [000001_init_schema.down.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.down.sql)
- Ensure clean teardown order for tables and composite constraints.

---

### Component 3: Credential Redaction Utility & Logging

#### [NEW] [redact.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/logger/redact.go)
- Implement `RedactURL(rawURL string) string` helper to parse connection URLs and replace userinfo passwords with `*****`.

#### [NEW] [redact_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/logger/redact_test.go)
- Unit tests verifying credential redaction for Redis URLs (`redis://:pass@host:6379`) and Postgres DSNs.

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)
#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/worker/main.go)
- Wrap Redis and Postgres connection log outputs with `logger.RedactURL(...)`.

---

## Verification Plan

### Automated Tests
- Run `go test -v -race ./...` to verify all platform and domain tests pass cleanly.

### Manual Database Verification
- Execute migration schema against local test database.
- Attempt cross-tenant insert test: Verify PostgreSQL rejects inserting a `workflow_versions` row where `tenant_id` does not match `workflows.tenant_id`.
- Attempt cross-version edge insert test: Verify PostgreSQL rejects connecting nodes across different `workflow_version_id`s.

-- 000001_init_schema.up.sql
-- FlowForge Database Migration v1

-- 1. tenants
CREATE TABLE IF NOT EXISTS tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug VARCHAR(255) NOT NULL UNIQUE,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. users
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL CHECK (role IN ('admin', 'editor', 'viewer')),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_users_tenant_email UNIQUE (tenant_id, email)
);

CREATE INDEX IF NOT EXISTS idx_users_tenant_role ON users(tenant_id, role);

-- 3. workflows
CREATE TABLE IF NOT EXISTS workflows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status VARCHAR(50) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived', 'disabled')),
    current_version_number INT NOT NULL DEFAULT 1,
    current_version_id UUID, -- Foreign key added after workflow_versions table created
    row_version INT NOT NULL DEFAULT 1 CHECK (row_version >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_workflows_tenant_name UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_workflows_tenant_status_updated ON workflows(tenant_id, status, updated_at DESC);

-- 4. workflow_versions
CREATE TABLE IF NOT EXISTS workflow_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_id UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    version_number INT NOT NULL CHECK (version_number > 0),
    status VARCHAR(50) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
    graph_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    checksum VARCHAR(64) NOT NULL DEFAULT '',
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_workflow_versions_workflow_ver UNIQUE (workflow_id, version_number)
);

CREATE INDEX IF NOT EXISTS idx_workflow_versions_tenant_wf_ver ON workflow_versions(tenant_id, workflow_id, version_number DESC);

-- Add circular foreign key for workflows.current_version_id
ALTER TABLE workflows
    ADD CONSTRAINT fk_workflows_current_version
    FOREIGN KEY (current_version_id)
    REFERENCES workflow_versions(id)
    ON DELETE SET NULL;

-- 5. workflow_nodes
CREATE TABLE IF NOT EXISTS workflow_nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_version_id UUID NOT NULL REFERENCES workflow_versions(id) ON DELETE CASCADE,
    node_key VARCHAR(255) NOT NULL,
    node_type VARCHAR(50) NOT NULL CHECK (node_type IN ('HTTP', 'DELAY', 'CONDITION', 'TRANSFORM')),
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    position_x INT NOT NULL DEFAULT 0,
    position_y INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_workflow_nodes_version_key UNIQUE (workflow_version_id, node_key)
);

CREATE INDEX IF NOT EXISTS idx_workflow_nodes_tenant_version ON workflow_nodes(tenant_id, workflow_version_id);

-- 6. workflow_edges
CREATE TABLE IF NOT EXISTS workflow_edges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_version_id UUID NOT NULL REFERENCES workflow_versions(id) ON DELETE CASCADE,
    from_node_id UUID NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    to_node_id UUID NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_workflow_edges_no_self_loop CHECK (from_node_id != to_node_id),
    CONSTRAINT uq_workflow_edges_version_from_to UNIQUE (workflow_version_id, from_node_id, to_node_id)
);

CREATE INDEX IF NOT EXISTS idx_workflow_edges_version_from ON workflow_edges(workflow_version_id, from_node_id);
CREATE INDEX IF NOT EXISTS idx_workflow_edges_version_to ON workflow_edges(workflow_version_id, to_node_id);

-- 7. workflow_runs
CREATE TABLE IF NOT EXISTS workflow_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_id UUID NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    workflow_version_id UUID NOT NULL REFERENCES workflow_versions(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out')),
    trigger_type VARCHAR(50) NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('manual', 'webhook', 'cron')),
    idempotency_key VARCHAR(255),
    input_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    execution_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_tenant_wf_created ON workflow_runs(tenant_id, workflow_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_tenant_status_created ON workflow_runs(tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_tenant_ver_created ON workflow_runs(tenant_id, workflow_version_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_runs_tenant_idempotency ON workflow_runs(tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

-- 8. step_runs
CREATE TABLE IF NOT EXISTS step_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    workflow_node_id UUID NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    node_key VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'running', 'succeeded', 'failed', 'retrying', 'skipped')),
    attempt_count INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    input_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_payload JSONB,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_step_runs_run_node UNIQUE (workflow_run_id, workflow_node_id)
);

CREATE INDEX IF NOT EXISTS idx_step_runs_run_status ON step_runs(workflow_run_id, status);
CREATE INDEX IF NOT EXISTS idx_step_runs_tenant_run ON step_runs(tenant_id, workflow_run_id);

-- 9. execution_logs
CREATE TABLE IF NOT EXISTS execution_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_run_id UUID REFERENCES step_runs(id) ON DELETE CASCADE,
    level VARCHAR(20) NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message TEXT NOT NULL,
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_execution_logs_run_created ON execution_logs(workflow_run_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_execution_logs_tenant_run_created ON execution_logs(tenant_id, workflow_run_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_execution_logs_step_created ON execution_logs(step_run_id, created_at ASC);

-- 10. audit_logs
CREATE TABLE IF NOT EXISTS audit_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    action VARCHAR(255) NOT NULL,
    entity_type VARCHAR(255) NOT NULL,
    entity_id UUID,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_tenant_created ON audit_logs(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_tenant_entity ON audit_logs(tenant_id, entity_type, entity_id);

-- 11. idempotency_keys
CREATE TABLE IF NOT EXISTS idempotency_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scope VARCHAR(100) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    workflow_id UUID REFERENCES workflows(id) ON DELETE CASCADE,
    workflow_run_id UUID REFERENCES workflow_runs(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_idempotency_keys_tenant_scope_key UNIQUE (tenant_id, scope, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires ON idempotency_keys(expires_at);

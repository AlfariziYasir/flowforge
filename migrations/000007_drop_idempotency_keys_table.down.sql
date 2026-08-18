CREATE TABLE IF NOT EXISTS idempotency_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scope VARCHAR(100) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    workflow_id UUID,
    workflow_run_id UUID,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_idempotency_keys_tenant_scope_key UNIQUE (tenant_id, scope, idempotency_key),
    CONSTRAINT fk_idempotency_keys_tenant_wf FOREIGN KEY (tenant_id, workflow_id) REFERENCES workflows(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_idempotency_keys_tenant_run FOREIGN KEY (tenant_id, workflow_run_id) REFERENCES workflow_runs(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires ON idempotency_keys(expires_at);

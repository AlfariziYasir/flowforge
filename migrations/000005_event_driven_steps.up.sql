-- Migration 000005: Event-driven steps (case B) — waiting statuses, wait tokens,
-- orphan events, and the per-tenant webhook secret.
-- Phase 7 (v2): protocol-agnostic core. No outbox/inbox by design decision — the
-- token's own consumed_at is the dedup record and wake-side reliability rides the
-- existing ReclaimStalePendingRuns (design_event_driven_steps.md, v2 §2).

-- A run parks itself in 'waiting' while an EVENT_WAIT step waits for an event.
ALTER TABLE workflow_runs DROP CONSTRAINT IF EXISTS workflow_runs_status_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_status_check
    CHECK (status IN ('pending', 'running', 'waiting', 'succeeded', 'failed', 'canceled', 'timed_out'));

ALTER TABLE workflow_nodes DROP CONSTRAINT IF EXISTS workflow_nodes_node_type_check;
ALTER TABLE workflow_nodes ADD CONSTRAINT workflow_nodes_node_type_check
    CHECK (node_type IN ('HTTP', 'DELAY', 'CONDITION', 'TRANSFORM', 'EVENT_PUBLISH', 'EVENT_WAIT'));

-- Per-tenant webhook secret for the event ingress adapters (HTTP/gRPC/NATS all
-- verify signatures against this). Null until a tenant rotates one in.
ALTER TABLE tenants ADD COLUMN webhook_secret VARCHAR(255);

-- A wait token correlates an inbound event to the run and step parked on it.
CREATE TABLE step_wait_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_run_id UUID NOT NULL,
    step_run_id     UUID NOT NULL,
    correlation_key VARCHAR(255) NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_wait_token UNIQUE (tenant_id, correlation_key),
    CONSTRAINT fk_wait_token_tenant_run FOREIGN KEY (tenant_id, workflow_run_id)
        REFERENCES workflow_runs(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_wait_tokens_expiry ON step_wait_tokens (expires_at) WHERE consumed_at IS NULL;

-- Events that matched no wait token are never dropped silently: they land here
-- (dead-letter) so a correlation-key bug is discoverable and countable.
CREATE TABLE orphan_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    correlation_key VARCHAR(255) NOT NULL,
    payload         JSONB NOT NULL,
    reason          VARCHAR(255) NOT NULL,
    arrived_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

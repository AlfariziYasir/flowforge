-- Migration 000004: Retry lineage for workflow_runs.
-- Phase 6 execution API: a retried run traces back to the run it retried, so
-- history (list/get) can show it without inferring it from timing or matching
-- workflow_version_id alone. Nullable: a run created by a normal trigger has none.

ALTER TABLE workflow_runs
    ADD COLUMN retried_from_run_id UUID;

-- Composite FK against uq_workflow_runs_tenant_id (tenant_id, id), matching the
-- project's tenant-scoped-FK convention. Column-scoped ON DELETE SET NULL requires
-- PostgreSQL 15+ (see migrations/000001's fk_workflows_current_version for the
-- same pattern and the same floor).
ALTER TABLE workflow_runs
    ADD CONSTRAINT fk_workflow_runs_tenant_retried_from
    FOREIGN KEY (tenant_id, retried_from_run_id)
    REFERENCES workflow_runs(tenant_id, id)
    ON DELETE SET NULL (retried_from_run_id);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_retried_from
    ON workflow_runs (tenant_id, retried_from_run_id)
    WHERE retried_from_run_id IS NOT NULL;

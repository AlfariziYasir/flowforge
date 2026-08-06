-- Migration 000004 (down): reverse retry lineage.
-- No production data is expected at this stage; the column is transient lineage
-- metadata with no downstream dependents, so a plain drop is safe.

DROP INDEX IF EXISTS idx_workflow_runs_retried_from;
ALTER TABLE workflow_runs DROP CONSTRAINT IF EXISTS fk_workflow_runs_tenant_retried_from;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS retried_from_run_id;

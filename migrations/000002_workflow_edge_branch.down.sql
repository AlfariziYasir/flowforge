-- Migration 000002 Down: Revert branch column and step_runs status CHECK widening.
-- Note: Reversion assumes no production rows exist with non-default branches or 'waiting' statuses.

ALTER TABLE step_runs DROP CONSTRAINT IF EXISTS chk_step_runs_status;
ALTER TABLE step_runs ADD CONSTRAINT step_runs_status_check
    CHECK (status IN ('pending', 'ready', 'running', 'succeeded', 'failed', 'retrying', 'skipped'));

ALTER TABLE workflow_edges DROP CONSTRAINT IF EXISTS uq_workflow_edges_version_from_to_branch;
ALTER TABLE workflow_edges ADD CONSTRAINT uq_workflow_edges_version_from_to
    UNIQUE (workflow_version_id, from_node_id, to_node_id);

ALTER TABLE workflow_edges DROP CONSTRAINT IF EXISTS chk_workflow_edges_branch;
ALTER TABLE workflow_edges DROP COLUMN IF EXISTS branch;

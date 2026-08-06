-- Migration 000003 (down): reverse the worker claiming columns and node type widening.
-- No production data is expected at this stage; leases and claims are transient worker state.

DROP INDEX IF EXISTS idx_workflow_runs_lease_expiry;

ALTER TABLE workflow_nodes DROP CONSTRAINT IF EXISTS workflow_nodes_node_type_check;
ALTER TABLE workflow_nodes ADD CONSTRAINT workflow_nodes_node_type_check
    CHECK (node_type IN ('HTTP', 'DELAY', 'CONDITION', 'TRANSFORM'));

ALTER TABLE workflow_runs DROP COLUMN IF EXISTS lease_expires_at;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS claimed_by;

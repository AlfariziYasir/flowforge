-- Migration 000003: Worker claiming columns for workflow_runs & EVENT_PUBLISH node type.
-- Phase 5 worker runtime: atomic run claims carry a worker identity and a lease that a
-- reaper reclaims after expiry (crash recovery, D-7). EVENT_PUBLISH is the node type the
-- event-driven design introduces (D-5).

-- Worker identity + lease for the atomic run claim. NULL when the run is not claimed.
ALTER TABLE workflow_runs
    ADD COLUMN claimed_by VARCHAR(255);
ALTER TABLE workflow_runs
    ADD COLUMN lease_expires_at TIMESTAMPTZ;

-- Widen workflow_nodes.node_type to admit EVENT_PUBLISH.
ALTER TABLE workflow_nodes DROP CONSTRAINT IF EXISTS workflow_nodes_node_type_check;
ALTER TABLE workflow_nodes ADD CONSTRAINT workflow_nodes_node_type_check
    CHECK (node_type IN ('HTTP', 'DELAY', 'CONDITION', 'TRANSFORM', 'EVENT_PUBLISH'));

-- Reaper index: expired leases must be found without scanning every run.
CREATE INDEX IF NOT EXISTS idx_workflow_runs_lease_expiry
    ON workflow_runs (status, lease_expires_at)
    WHERE status = 'running' AND lease_expires_at IS NOT NULL;

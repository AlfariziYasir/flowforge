-- Migration 000002: Add branch discriminator to workflow_edges & widen step_runs status CHECK constraint.
-- See design decision D-1 and D-5 in phase_4_workflow_engine_core.md.

ALTER TABLE workflow_edges
    ADD COLUMN branch VARCHAR(50) NOT NULL DEFAULT 'default';

ALTER TABLE workflow_edges
    ADD CONSTRAINT chk_workflow_edges_branch CHECK (branch IN ('default', 'true', 'false'));

-- A CONDITION node may legitimately have two edges to the same target, one per branch.
ALTER TABLE workflow_edges DROP CONSTRAINT IF EXISTS uq_workflow_edges_version_from_to;
ALTER TABLE workflow_edges ADD CONSTRAINT uq_workflow_edges_version_from_to_branch
    UNIQUE (workflow_version_id, from_node_id, to_node_id, branch);

-- Widening step_runs status CHECK to include 'waiting' for event-driven step readiness.
ALTER TABLE step_runs DROP CONSTRAINT IF EXISTS step_runs_status_check;
ALTER TABLE step_runs ADD CONSTRAINT chk_step_runs_status
    CHECK (status IN ('pending', 'ready', 'running', 'waiting', 'succeeded', 'failed', 'retrying', 'skipped'));

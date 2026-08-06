-- Migration 000005 (down): reverse event-driven steps.
-- No production data is expected at this stage; wait tokens and orphan events
-- are execution-time state. Narrowing the CHECK constraints fails if any row
-- already holds the widened values — acceptable here because the tables are
-- dropped first, so no such row can exist.

DROP TABLE IF EXISTS orphan_events;
DROP TABLE IF EXISTS step_wait_tokens;

ALTER TABLE tenants DROP COLUMN IF EXISTS webhook_secret;

ALTER TABLE workflow_nodes DROP CONSTRAINT IF EXISTS workflow_nodes_node_type_check;
ALTER TABLE workflow_nodes ADD CONSTRAINT workflow_nodes_node_type_check
    CHECK (node_type IN ('HTTP', 'DELAY', 'CONDITION', 'TRANSFORM', 'EVENT_PUBLISH'));

ALTER TABLE workflow_runs DROP CONSTRAINT IF EXISTS workflow_runs_status_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_status_check
    CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out'));

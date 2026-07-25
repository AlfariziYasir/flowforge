-- 000001_init_schema.down.sql
-- Teardown all tables in reverse dependency order

ALTER TABLE IF EXISTS workflows DROP CONSTRAINT IF EXISTS fk_workflows_current_version;

DROP TABLE IF EXISTS idempotency_keys CASCADE;
DROP TABLE IF EXISTS audit_logs CASCADE;
DROP TABLE IF EXISTS execution_logs CASCADE;
DROP TABLE IF EXISTS step_runs CASCADE;
DROP TABLE IF EXISTS workflow_runs CASCADE;
DROP TABLE IF EXISTS workflow_edges CASCADE;
DROP TABLE IF EXISTS workflow_nodes CASCADE;
DROP TABLE IF EXISTS workflow_versions CASCADE;
DROP TABLE IF EXISTS workflows CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS tenants CASCADE;

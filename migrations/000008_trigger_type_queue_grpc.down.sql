ALTER TABLE workflow_runs DROP CONSTRAINT IF EXISTS workflow_runs_trigger_type_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_trigger_type_check CHECK (trigger_type IN ('manual', 'webhook', 'cron'));

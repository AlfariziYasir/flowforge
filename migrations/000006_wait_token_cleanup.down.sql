DROP INDEX IF EXISTS uq_wait_token_active;
ALTER TABLE step_wait_tokens DROP COLUMN IF EXISTS handled_at;
ALTER TABLE step_wait_tokens ADD CONSTRAINT uq_wait_token UNIQUE (tenant_id, correlation_key);

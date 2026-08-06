-- Add handled_at column to step_wait_tokens and scope the unique index to active tokens only.
ALTER TABLE step_wait_tokens ADD COLUMN handled_at TIMESTAMPTZ;

-- Drop old full unique constraint and replace with partial unique index.
ALTER TABLE step_wait_tokens DROP CONSTRAINT IF EXISTS uq_wait_token;
DROP INDEX IF EXISTS uq_wait_token_active;

CREATE UNIQUE INDEX uq_wait_token_active ON step_wait_tokens (tenant_id, correlation_key)
    WHERE consumed_at IS NULL AND handled_at IS NULL;

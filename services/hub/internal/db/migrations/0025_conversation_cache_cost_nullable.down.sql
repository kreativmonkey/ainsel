-- Rolling back re-flattens absence into a measured zero, so the NULLs have to
-- be collapsed to 0 before the NOT NULL constraint can be restored. This is
-- lossy by definition: the down migration cannot preserve a distinction the
-- old schema has no encoding for.
UPDATE task_conversations SET
    cache_read_tokens  = COALESCE(cache_read_tokens, 0),
    cache_write_tokens = COALESCE(cache_write_tokens, 0),
    cost_usd           = COALESCE(cost_usd, 0);
ALTER TABLE task_conversations
    ALTER COLUMN cache_read_tokens  SET DEFAULT 0,
    ALTER COLUMN cache_read_tokens  SET NOT NULL,
    ALTER COLUMN cache_write_tokens SET DEFAULT 0,
    ALTER COLUMN cache_write_tokens SET NOT NULL,
    ALTER COLUMN cost_usd           SET DEFAULT 0,
    ALTER COLUMN cost_usd           SET NOT NULL;

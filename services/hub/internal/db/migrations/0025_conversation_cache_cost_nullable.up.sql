-- Unreported cache usage and cost were indistinguishable from measured zeros
-- (issue #75): a provider that never reports prompt-cache figures produced the
-- same rows as one that reports cached_tokens: 0, so every surface asserted a
-- measured 0 where nothing was measured. NULL is the only encoding that
-- survives SUM: the read paths keep their COALESCE(SUM(...), 0) numerics and
-- gain honest presence flags from COUNT(col) > 0.
--
-- The ingest path (tasklogs.ConversationMessage) now writes NULL for absent
-- figures and a real number — including a reported 0 — otherwise.
--
-- Backfill: every row stored before this migration was written by an ingest
-- path that flattened absence into 0, so every stored 0 is ambiguous and must
-- read as "not reported". Leaving it in place would make the new COUNT(col) > 0
-- presence flags claim legacy rows as measured zeros — the exact #75 symptom,
-- and what the live platform's rows all are today. The migration is itself the
-- timestamp boundary: no row has been written by the presence-aware path yet,
-- so NULLing the zeros discards nothing that was ever measured; non-zero
-- figures are reported values and are kept. Each column is NULLed on its own,
-- so a measured non-zero cache_read survives on a row whose cache_write is an
-- ambiguous zero.
ALTER TABLE task_conversations
    ALTER COLUMN cache_read_tokens  DROP DEFAULT,
    ALTER COLUMN cache_read_tokens  DROP NOT NULL,
    ALTER COLUMN cache_write_tokens DROP DEFAULT,
    ALTER COLUMN cache_write_tokens DROP NOT NULL,
    ALTER COLUMN cost_usd           DROP DEFAULT,
    ALTER COLUMN cost_usd           DROP NOT NULL;

UPDATE task_conversations SET cache_read_tokens  = NULL WHERE cache_read_tokens  = 0;
UPDATE task_conversations SET cache_write_tokens = NULL WHERE cache_write_tokens = 0;
UPDATE task_conversations SET cost_usd           = NULL WHERE cost_usd           = 0;

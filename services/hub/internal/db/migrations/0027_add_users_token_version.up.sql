-- Per-user token version for local session JWT invalidation (issue #112).
-- Embedded as the "token_version" claim in local session tokens; the
-- localauth middleware rejects tokens whose claim is older than this value.
-- Bumped atomically with every password set/reset (authz.Store.SetPassword)
-- and by explicit sign-out-everywhere (authz.Store.BumpTokenVersion).
-- BIGINT removes any overflow concern for a user-triggerable counter.
ALTER TABLE users ADD COLUMN token_version BIGINT NOT NULL DEFAULT 0;

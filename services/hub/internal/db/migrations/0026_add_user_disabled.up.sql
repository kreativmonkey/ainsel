-- Disable/enable users without deleting them (issue #117).
-- A disabled user cannot log in and all API access is rejected, but group
-- memberships and user tokens are kept intact so re-enabling restores access.
ALTER TABLE users ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT false;

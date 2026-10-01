-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-449: every harness session and every run carries its own revision, so a
-- client that holds several copies of one row (a list, a detail, a launch result)
-- keeps the newest by asking the row, never the order the answers arrived in.
-- The trigger bumps the version inside the statement that changes the row, so it
-- commits or rolls back with that change and no writer can forget it. Versions
-- only ever grow: the new value derives from the old one under the row lock.
-- This is separate from harness_sessions.revision, which is an optimistic-lock
-- token that only some writers advance.
SET LOCAL lock_timeout = '5s';

-- A constant default adds the column without rewriting existing rows. NOT VALID
-- avoids scanning hot tables while ALTER TABLE holds ACCESS EXCLUSIVE.
ALTER TABLE harness_sessions ADD COLUMN row_version bigint NOT NULL DEFAULT 1;
ALTER TABLE agent_runs ADD COLUMN row_version bigint NOT NULL DEFAULT 1;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_sessions_row_version_check CHECK (row_version > 0) NOT VALID;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_row_version_check CHECK (row_version > 0) NOT VALID;
-- Validation runs in 1054, after this transaction releases its DDL locks.

CREATE FUNCTION aeon_bump_row_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.row_version := OLD.row_version + 1;
    RETURN NEW;
END;
$$;

CREATE TRIGGER harness_sessions_row_version BEFORE UPDATE ON harness_sessions
    FOR EACH ROW EXECUTE FUNCTION aeon_bump_row_version();
CREATE TRIGGER agent_runs_row_version BEFORE UPDATE ON agent_runs
    FOR EACH ROW EXECUTE FUNCTION aeon_bump_row_version();

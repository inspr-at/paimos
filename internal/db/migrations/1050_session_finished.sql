-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- AEON-437. One authority for "this session finished its work": it reported all
-- of it (100%) and its launcher recorded a clean exit (stop reason
-- process_exited). Anything else that stopped is ended early or failed, and a
-- session that merely went quiet has no recorded exit at all. Screens read the
-- derived flag, never the stop reason, so a viewer who may not see the reason
-- still gets the same answer. The whole predicate is coalesced: a NULL stop
-- reason or percent is "not finished", never an unknown, so the flag is always a
-- boolean that every payload can carry.
CREATE FUNCTION aeon_session_finished(stopped_at timestamptz, stop_reason text, progress_pct smallint)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT coalesce(stopped_at IS NOT NULL AND stop_reason = 'process_exited' AND progress_pct >= 100, false);
$$;

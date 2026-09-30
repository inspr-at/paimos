-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
-- AEON-434. agent_pairing.reported tells the register page that the daemon
-- finished a setup step. It names a computer and a setup state, so it is
-- addressed to one person: the one who approved that pairing. The writer stores
-- that person's canonical principal in the event metadata; this policy lets a
-- caller read the row only when its own principal or canonical person matches,
-- on the history list and the stream alike. Other workspace viewers and agent
-- keys read no such event. Explicit service paths (system, never a principal)
-- pass so the writer can return the row it inserts. Other event types are
-- untouched.
CREATE POLICY events_pairing_report_audience ON events AS RESTRICTIVE FOR SELECT
    USING (type <> 'agent_pairing.reported'
        OR (SELECT aeon_visibility_system())
        OR (metadata ->> 'audience_principal_id') = ANY ((SELECT aeon_current_principals())::text[]));

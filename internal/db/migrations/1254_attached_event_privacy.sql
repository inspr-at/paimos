-- SPDX-License-Identifier: AGPL-3.0-only
-- Workspace/project administrators do not inherit private owner-note history.
CREATE POLICY attached_note_event_privacy ON events AS RESTRICTIVE FOR SELECT
 USING (type NOT LIKE 'inbox.attached_%' OR aeon_visibility_system()
        OR ("after"->>'sender_principal_id')=ANY(aeon_current_principals()::text[]));

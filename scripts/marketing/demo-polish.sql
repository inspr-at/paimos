-- PAI-746: enrich the LOCAL dev demo workspace for marketing captures.
-- Runs against the gitignored data/paimos.db only. Nothing here is
-- committed, and nothing is copied from a live instance.

-- 1. Give the demo operator a human identity. The dashboard greeting
--    uses first_name; the sidebar falls back through nickname.
UPDATE users
   SET first_name = 'Mara', last_name = 'Ellis', nickname = 'Mara Ellis'
 WHERE username = 'dev_admin';

-- 2. Kill the seven identical "Reporting CSV export" rows that dominate
--    Recent Issues and read as broken fixture data.
UPDATE issues SET title = 'Export retainer report as CSV'            WHERE project_id = 2 AND issue_number = 34;
UPDATE issues SET title = 'Portal: saved filters per customer'        WHERE project_id = 2 AND issue_number = 35;
UPDATE issues SET title = 'Invoice run misses partial-month entries'  WHERE project_id = 2 AND issue_number = 36;
UPDATE issues SET title = 'Acceptance mail should attach the PDF'     WHERE project_id = 2 AND issue_number = 37;
UPDATE issues SET title = 'Time entries: bulk re-assign to cost unit' WHERE project_id = 2 AND issue_number = 38;
UPDATE issues SET title = 'Budget burn-down chart for Q3 retainer'    WHERE project_id = 2 AND issue_number = 39;
UPDATE issues SET title = 'Portal login should honour SSO domains'    WHERE project_id = 2 AND issue_number = 40;

-- 3. A finished Voice Intake session, so the flagship 5.x surface shows
--    the product working instead of an empty state. Artifacts mirror
--    exactly what the orchestrator writes (spec / summaries /
--    ticket_preview / impacts / project_match).
DELETE FROM intake_events WHERE session_id IN (SELECT id FROM intake_sessions WHERE user_id = 9001);
DELETE FROM intake_sessions WHERE user_id = 9001;

INSERT INTO intake_sessions
  (id, user_id, status, language, detected_project_id, detected_score,
   transcript, transcript_bytes, rev, session_prompt_tokens, session_completion_tokens,
   created_at, updated_at)
VALUES
  (1, 9001, 'active', 'en', 2, 94,
   'So the thing customers keep asking for is saved filters in the portal.' || char(10) ||
   'Right now every time they come back they re-pick the same project and the same status filter, every single visit.' || char(10) ||
   'I want them to be able to name a filter and pin it, and have it still be there next week.' || char(10) ||
   'It should be per user, not per company — two people at the same customer will care about different things.',
   402, 9, 4120, 1880,
   datetime('now','-14 minutes'), datetime('now','-2 minutes'));

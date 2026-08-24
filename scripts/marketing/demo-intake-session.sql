-- A finished Voice Intake session, so the flagship 5.x surface shows the
-- product working instead of an empty state. Events and artifacts are applied
-- by demo-intake-events.sql in the next transaction.
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

-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1628: old writers can omit both projections; no historical nags are backfilled.
CREATE TABLE desk_reminder_settings (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 default_after_seconds integer NOT NULL,
 time_zone text NOT NULL,
 quiet_start integer NOT NULL,
 quiet_end integer NOT NULL,
 revision bigint NOT NULL
);
ALTER TABLE desk_reminder_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE desk_reminder_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON desk_reminder_settings
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE TABLE desk_question_reminders (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 project_id uuid NOT NULL,
 question_id uuid NOT NULL,
 after_seconds integer NOT NULL,
 next_at timestamptz,
 last_at timestamptz,
 reminder_count bigint NOT NULL DEFAULT 0,
 PRIMARY KEY (tenant_id,question_id),
 FOREIGN KEY (tenant_id,project_id,question_id) REFERENCES desk_questions(tenant_id,project_id,node_id)
);
CREATE INDEX desk_question_reminders_due ON desk_question_reminders(tenant_id,next_at,question_id) WHERE next_at IS NOT NULL;
ALTER TABLE desk_question_reminders ENABLE ROW LEVEL SECURITY;
ALTER TABLE desk_question_reminders FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON desk_question_reminders
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_visibility ON desk_question_reminders AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

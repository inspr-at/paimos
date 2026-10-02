-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-573: durable tenant/project-scoped schedules and idempotent receipts.
SET LOCAL lock_timeout = '5s';
CREATE TABLE recurrences (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL,
 parent_id uuid NOT NULL,
 template jsonb NOT NULL CHECK (jsonb_typeof(template)='object'),
 trigger jsonb NOT NULL CHECK (jsonb_typeof(trigger)='object' AND trigger->>'kind' IN ('time','event')),
 queue_each boolean NOT NULL DEFAULT false,
 overlap_policy text NOT NULL DEFAULT 'skip' CHECK (overlap_policy IN ('skip','create')),
 catch_up_policy text NOT NULL DEFAULT 'one' CHECK (catch_up_policy='one'),
 paused boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 occurrence_count bigint NOT NULL DEFAULT 0 CHECK (occurrence_count>=0),
 next_at timestamptz,
 event_cursor bigint NOT NULL DEFAULT 0 CHECK (event_cursor>=0),
 created_by_principal_id uuid NOT NULL,
 active_since timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,parent_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,created_by_principal_id) REFERENCES principals(tenant_id,id),
 CHECK ((trigger->>'kind'='time' AND next_at IS NOT NULL) OR (trigger->>'kind'='event' AND next_at IS NULL))
);
ALTER TABLE recurrences ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurrences FORCE ROW LEVEL SECURITY;
CREATE POLICY recurrences_tenant ON recurrences
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY recurrences_project ON recurrences AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));
CREATE INDEX recurrences_due ON recurrences (tenant_id,next_at,id) WHERE NOT paused;
CREATE INDEX recurrences_event ON recurrences (tenant_id,project_id,event_cursor) WHERE NOT paused AND trigger->>'kind'='event';

CREATE TABLE recurrence_occurrences (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 recurrence_id uuid NOT NULL,
 occurrence_key text NOT NULL CHECK (length(occurrence_key) BETWEEN 1 AND 256),
 number bigint NOT NULL CHECK (number>0),
 scheduled_at timestamptz NOT NULL,
 node_id uuid,
 source_event_id bigint,
 outcome text NOT NULL CHECK (outcome IN ('created','skipped')),
 reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,recurrence_id,occurrence_key),
 UNIQUE (tenant_id,recurrence_id,number),
 FOREIGN KEY (tenant_id,recurrence_id) REFERENCES recurrences(tenant_id,id),
 FOREIGN KEY (tenant_id,node_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,source_event_id) REFERENCES events(tenant_id,id),
 CHECK ((outcome='created' AND node_id IS NOT NULL AND reason='') OR (outcome='skipped' AND node_id IS NULL AND reason<>''))
);
ALTER TABLE recurrence_occurrences ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurrence_occurrences FORCE ROW LEVEL SECURITY;
CREATE POLICY recurrence_occurrences_tenant ON recurrence_occurrences
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY recurrence_occurrences_project ON recurrence_occurrences AS RESTRICTIVE
 USING (EXISTS(SELECT 1 FROM recurrences r WHERE r.tenant_id=recurrence_occurrences.tenant_id AND r.id=recurrence_occurrences.recurrence_id))
 WITH CHECK (EXISTS(SELECT 1 FROM recurrences r WHERE r.tenant_id=recurrence_occurrences.tenant_id AND r.id=recurrence_occurrences.recurrence_id));
CREATE INDEX recurrence_occurrences_nodes ON recurrence_occurrences (tenant_id,recurrence_id,node_id) WHERE node_id IS NOT NULL;

-- Publication replay uses the existing append-only event log. The producer
-- takes tenant and tree locks before appending, and the index is the final fence.
CREATE UNIQUE INDEX events_release_publication_identity ON events (tenant_id,(metadata->>'publication_key'))
 WHERE type='release.published' AND metadata->>'publication_key' IS NOT NULL;
CREATE INDEX events_release_publications ON events (tenant_id,node_id,id DESC) WHERE type='release.published';
CREATE UNIQUE INDEX principals_recurring_work_actor ON principals (tenant_id)
 WHERE kind='agent' AND name='Recurring work' AND roles @> ARRAY['recurring_work']::text[];

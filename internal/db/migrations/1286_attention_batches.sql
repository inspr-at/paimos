-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-914 expansion: older writers never write this new table.
-- Preview identities and progress are durable, actor-bound and expire after 24h.
SET LOCAL lock_timeout = '5s';
CREATE TABLE attention_batches (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 actor_id uuid NOT NULL,
 action text NOT NULL,
 scope jsonb NOT NULL,
 through_event_id bigint NOT NULL,
 item_events bigint[] NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 undone_at timestamptz,
 snapshot jsonb NOT NULL,
 idempotency_key text,
 request jsonb,
 progress jsonb,
 undo_progress jsonb,
 completed_at timestamptz,
 PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX attention_batches_idempotency ON attention_batches(tenant_id,actor_id,idempotency_key)
 WHERE idempotency_key IS NOT NULL;
CREATE INDEX attention_batches_expiry ON attention_batches(tenant_id,created_at);
ALTER TABLE attention_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE attention_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY attention_batches_tenant ON attention_batches
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
-- Snapshots contain ticket keys and revisions: only the actor or a current
-- settings manager can see them. Item mutations still use node/event RLS.
CREATE POLICY attention_batches_actor ON attention_batches AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR actor_id=ANY((SELECT aeon_current_principals())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR actor_id=ANY((SELECT aeon_current_principals())::uuid[]));

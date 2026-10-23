-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1703: additive facts on the existing approval/Desk source. Older writers
-- need neither table; no existing permission, route or request shape changes.
CREATE TABLE action_approval_kinds (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{0,47}$'),
 label text NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 120),
 enabled boolean NOT NULL,
 revision bigint NOT NULL CHECK (revision > 0),
 PRIMARY KEY (tenant_id,kind)
);
ALTER TABLE action_approval_kinds ENABLE ROW LEVEL SECURITY;
ALTER TABLE action_approval_kinds FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON action_approval_kinds
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE TABLE action_approval_requests (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL,
 agent_principal_id uuid NOT NULL,
 approval_id uuid,
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 kind text NOT NULL,
 what text NOT NULL CHECK (length(btrim(what)) BETWEEN 1 AND 500),
 why text NOT NULL CHECK (length(btrim(why)) BETWEEN 1 AND 2000),
 exact_effect text NOT NULL CHECK (length(btrim(exact_effect)) BETWEEN 1 AND 4000),
 reversibility text NOT NULL CHECK (reversibility IN ('reversible','partial','irreversible')),
 reversal_plan text NOT NULL DEFAULT '' CHECK (length(reversal_plan)<=2000),
 human_input text NOT NULL CHECK (human_input IN ('none','secrets','tokens','manual_steps')),
 request_digest_sha256 text NOT NULL CHECK (request_digest_sha256 ~ '^[a-f0-9]{64}$'),
 expires_at timestamptz NOT NULL,
 requested_at timestamptz NOT NULL DEFAULT now(),
 state text NOT NULL CHECK (state IN ('pending','approved','denied','expired','refused')),
 reason text NOT NULL DEFAULT '',
 resolved_at timestamptz,
 decided_by_principal_id uuid,
 outcome_message_id uuid,
 PRIMARY KEY (tenant_id,id),
 UNIQUE (tenant_id,agent_principal_id,idempotency_key),
 UNIQUE (tenant_id,approval_id),
 FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
 FOREIGN KEY (tenant_id,agent_principal_id) REFERENCES principals(tenant_id,id),
 FOREIGN KEY (tenant_id,approval_id) REFERENCES approval_requests(tenant_id,id),
 FOREIGN KEY (tenant_id,decided_by_principal_id) REFERENCES principals(tenant_id,id),
 CHECK (expires_at>requested_at),
 CHECK ((state='pending')=(resolved_at IS NULL)),
 CHECK (state='pending' OR length(btrim(reason))>0),
 CHECK ((state='refused')=(approval_id IS NULL))
);
CREATE INDEX action_approval_expiry ON action_approval_requests(tenant_id,expires_at,id) WHERE state='pending';
ALTER TABLE action_approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE action_approval_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON action_approval_requests
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY project_visibility ON action_approval_requests AS RESTRICTIVE
 USING ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
 WITH CHECK ((SELECT aeon_visible_all()) OR project_id=ANY((SELECT aeon_visible_projects())::uuid[]));

CREATE FUNCTION aeon_guard_action_approval_facts() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.tenant_id,NEW.id,NEW.project_id,NEW.agent_principal_id,NEW.approval_id,NEW.idempotency_key,
   NEW.kind,NEW.what,NEW.why,NEW.exact_effect,NEW.reversibility,NEW.reversal_plan,NEW.human_input,
   NEW.request_digest_sha256,NEW.expires_at,NEW.requested_at)
 IS DISTINCT FROM ROW(OLD.tenant_id,OLD.id,OLD.project_id,OLD.agent_principal_id,OLD.approval_id,OLD.idempotency_key,
   OLD.kind,OLD.what,OLD.why,OLD.exact_effect,OLD.reversibility,OLD.reversal_plan,OLD.human_input,
   OLD.request_digest_sha256,OLD.expires_at,OLD.requested_at)
 OR OLD.state<>'pending' OR NEW.state NOT IN ('approved','denied','expired') THEN
  RAISE EXCEPTION 'action approval facts and terminal outcomes are immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER action_approval_facts_immutable BEFORE UPDATE ON action_approval_requests
 FOR EACH ROW EXECUTE FUNCTION aeon_guard_action_approval_facts();

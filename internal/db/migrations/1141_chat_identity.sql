-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-618 R1. No backfill, native process ownership or delivery activation.
CREATE TABLE chat_roles (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    owner_person_id uuid NOT NULL,
    project_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('lead','worker')),
    slot_key text NOT NULL CHECK (length(slot_key) BETWEEN 1 AND 128 AND slot_key ~ '^[a-z0-9][a-z0-9._:/-]*$'),
    conversation_scope text NOT NULL DEFAULT 'person_project' CHECK (conversation_scope='person_project'),
    binding_epoch bigint NOT NULL DEFAULT 0 CHECK (binding_epoch>=0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,owner_person_id,project_id,slot_key),
    UNIQUE (tenant_id,id,project_id,owner_person_id),
    FOREIGN KEY (tenant_id,owner_person_id) REFERENCES principals(tenant_id,id),
    FOREIGN KEY (tenant_id,project_id) REFERENCES nodes(tenant_id,id),
    CHECK ((kind='lead' AND slot_key='lead') OR (kind='worker' AND slot_key NOT IN ('lead','worker')))
);
CREATE TABLE chat_threads (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    role_id uuid NOT NULL,
    person_id uuid NOT NULL,
    project_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
    archived_at timestamptz,
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,person_id,role_id),
    FOREIGN KEY (tenant_id,role_id,project_id,person_id) REFERENCES chat_roles(tenant_id,id,project_id,owner_person_id)
);
CREATE TABLE chat_session_bindings (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    role_id uuid NOT NULL,
    project_id uuid NOT NULL,
    owner_person_id uuid NOT NULL,
    session_id uuid NOT NULL,
    agent_principal_id uuid NOT NULL,
    binding_epoch bigint NOT NULL CHECK (binding_epoch>0),
    valid_from timestamptz NOT NULL DEFAULT clock_timestamp(),
    valid_to timestamptz,
    reason text NOT NULL CHECK (reason IN ('selected','handover')),
    PRIMARY KEY (tenant_id,role_id,binding_epoch),
    FOREIGN KEY (tenant_id,role_id,project_id,owner_person_id) REFERENCES chat_roles(tenant_id,id,project_id,owner_person_id),
    FOREIGN KEY (tenant_id,project_id,session_id) REFERENCES harness_sessions(tenant_id,project_id,id),
    FOREIGN KEY (tenant_id,agent_principal_id) REFERENCES principals(tenant_id,id),
    CHECK (valid_to IS NULL OR valid_to>=valid_from)
);
CREATE UNIQUE INDEX chat_binding_active_role ON chat_session_bindings(tenant_id,role_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX chat_binding_active_session ON chat_session_bindings(tenant_id,session_id) WHERE valid_to IS NULL;
CREATE INDEX chat_binding_session_history ON chat_session_bindings(tenant_id,session_id,binding_epoch);

-- A native context never changes its private conversation identity, even
-- after handover or a change of registration ownership. The unique session
-- key also fences bindings whose previous owner is hidden by RLS.
CREATE TABLE chat_session_contexts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    session_id uuid NOT NULL,
    role_id uuid NOT NULL,
    owner_person_id uuid NOT NULL,
    project_id uuid NOT NULL,
    PRIMARY KEY (tenant_id,session_id),
    UNIQUE (tenant_id,session_id,role_id,owner_person_id),
    FOREIGN KEY (tenant_id,project_id,session_id) REFERENCES harness_sessions(tenant_id,project_id,id),
    FOREIGN KEY (tenant_id,role_id,project_id,owner_person_id) REFERENCES chat_roles(tenant_id,id,project_id,owner_person_id)
);
ALTER TABLE chat_session_bindings ADD CONSTRAINT chat_binding_native_context
    FOREIGN KEY (tenant_id,session_id,role_id,owner_person_id) REFERENCES chat_session_contexts(tenant_id,session_id,role_id,owner_person_id);
ALTER TABLE chat_session_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_session_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_session_contexts_tenant ON chat_session_contexts USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_session_contexts_participant ON chat_session_contexts AS RESTRICTIVE USING (
    owner_person_id=ANY((SELECT aeon_current_principals()))
    OR session_id=NULLIF(current_setting('aeon.chat_session_id',true),'')::uuid);

CREATE FUNCTION aeon_chat_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['binding_epoch','revision','archived_at','valid_to'])
        IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['binding_epoch','revision','archived_at','valid_to']) THEN
      RAISE EXCEPTION 'chat participant identity is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER chat_roles_identity BEFORE UPDATE OR DELETE ON chat_roles FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();
CREATE TRIGGER chat_threads_identity BEFORE UPDATE OR DELETE ON chat_threads FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();
CREATE TRIGGER chat_contexts_identity BEFORE UPDATE OR DELETE ON chat_session_contexts FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();
CREATE TRIGGER chat_bindings_identity BEFORE UPDATE OR DELETE ON chat_session_bindings FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();

ALTER TABLE chat_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_roles FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_roles_tenant ON chat_roles USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_roles_participant ON chat_roles AS RESTRICTIVE USING (
    owner_person_id=ANY((SELECT aeon_current_principals()))
    OR id=NULLIF(current_setting('aeon.chat_role_id',true),'')::uuid);
ALTER TABLE chat_threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_threads FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_threads_tenant ON chat_threads USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_threads_participant ON chat_threads AS RESTRICTIVE USING (
    person_id=ANY((SELECT aeon_current_principals()))
    OR role_id=NULLIF(current_setting('aeon.chat_role_id',true),'')::uuid);
ALTER TABLE chat_session_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_session_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_session_bindings_tenant ON chat_session_bindings USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_session_bindings_participant ON chat_session_bindings AS RESTRICTIVE USING (
    owner_person_id=ANY((SELECT aeon_current_principals()))
    OR (valid_to IS NULL AND session_id=NULLIF(current_setting('aeon.chat_session_id',true),'')::uuid));

-- Only verified chat transactions set these local context values. Service
-- visibility and a shared principal alone never bypass the chat fence.
ALTER TABLE inbox_messages ADD COLUMN chat_thread_id uuid,
    ADD CONSTRAINT inbox_chat_thread_tenant FOREIGN KEY (tenant_id,chat_thread_id) REFERENCES chat_threads(tenant_id,id),
    ADD CONSTRAINT inbox_chat_body_bytes CHECK (chat_thread_id IS NULL OR octet_length(body)<=65536);
CREATE INDEX inbox_chat_thread_order ON inbox_messages(tenant_id,chat_thread_id,sent_event_id) WHERE chat_thread_id IS NOT NULL;
CREATE POLICY inbox_chat_participant ON inbox_messages AS RESTRICTIVE USING (
    chat_thread_id IS NULL OR (
      chat_thread_id=NULLIF(current_setting('aeon.chat_conversation_id',true),'')::uuid
      AND EXISTS(SELECT 1 FROM chat_threads t WHERE t.tenant_id=inbox_messages.tenant_id AND t.id=inbox_messages.chat_thread_id)));

-- Legacy projections/transport/receipts may never contain new-mode traffic,
-- even in an otherwise participant-authorized chat transaction.
CREATE FUNCTION aeon_chat_guard_legacy_projection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE message uuid;
BEGIN
    message := (to_jsonb(NEW)->>TG_ARGV[0])::uuid;
    IF message IS NOT NULL AND NOT EXISTS (
       SELECT 1 FROM inbox_messages WHERE tenant_id=NEW.tenant_id AND id=message AND chat_thread_id IS NULL) THEN
       RAISE EXCEPTION 'legacy transport cannot reference chat messages' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER chat_no_compat BEFORE INSERT OR UPDATE ON inbox_compat_messages FOR EACH ROW EXECUTE FUNCTION aeon_chat_guard_legacy_projection('inbox_message_id');
CREATE TRIGGER chat_no_receipts BEFORE INSERT OR UPDATE ON inbox_receipts FOR EACH ROW EXECUTE FUNCTION aeon_chat_guard_legacy_projection('message_id');
CREATE TRIGGER chat_no_wakes BEFORE INSERT OR UPDATE ON inbox_wakes FOR EACH ROW EXECUTE FUNCTION aeon_chat_guard_legacy_projection('message_id');
CREATE TRIGGER chat_no_managed_drain BEFORE INSERT OR UPDATE ON harness_deliveries FOR EACH ROW EXECUTE FUNCTION aeon_chat_guard_legacy_projection('message_id');
CREATE POLICY inbox_receipts_legacy_mode ON inbox_receipts AS RESTRICTIVE USING (EXISTS(SELECT 1 FROM inbox_messages m WHERE m.tenant_id=inbox_receipts.tenant_id AND m.id=inbox_receipts.message_id AND m.chat_thread_id IS NULL));
CREATE POLICY inbox_wakes_legacy_mode ON inbox_wakes AS RESTRICTIVE USING (EXISTS(SELECT 1 FROM inbox_messages m WHERE m.tenant_id=inbox_wakes.tenant_id AND m.id=inbox_wakes.message_id AND m.chat_thread_id IS NULL));
CREATE POLICY harness_deliveries_legacy_mode ON harness_deliveries AS RESTRICTIVE USING (EXISTS(SELECT 1 FROM inbox_messages m WHERE m.tenant_id=harness_deliveries.tenant_id AND m.id=harness_deliveries.message_id AND m.chat_thread_id IS NULL));

CREATE FUNCTION aeon_chat_guard_message_mode() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' AND NEW.chat_thread_id IS DISTINCT FROM OLD.chat_thread_id THEN
      RAISE EXCEPTION 'message conversation identity is immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.reply_to_id IS NOT NULL AND NOT EXISTS (
      SELECT 1 FROM inbox_messages parent WHERE parent.tenant_id=NEW.tenant_id AND parent.id=NEW.reply_to_id
        AND parent.chat_thread_id IS NOT DISTINCT FROM NEW.chat_thread_id) THEN
      RAISE EXCEPTION 'reply cannot cross conversation modes' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER inbox_chat_mode_immutable BEFORE INSERT OR UPDATE ON inbox_messages FOR EACH ROW EXECUTE FUNCTION aeon_chat_guard_message_mode();

-- Future chat events are private even before the stream package is mounted.
CREATE POLICY events_chat_private ON events AS RESTRICTIVE USING (
    type NOT LIKE 'chat.%' OR (
      coalesce(after->>'conversation_id',before->>'conversation_id')=NULLIF(current_setting('aeon.chat_conversation_id',true),'')
      AND EXISTS(SELECT 1 FROM chat_threads t WHERE t.tenant_id=events.tenant_id AND t.id::text=current_setting('aeon.chat_conversation_id',true))));

-- Extend the legacy SQL registry without changing its existing entries.
CREATE OR REPLACE FUNCTION aeon_authz_registry_permission(candidate text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
    SELECT EXISTS (
      SELECT 1 FROM (VALUES
        ('nodes','read write delete move restore configure'),
        ('kinds','read manage'), ('tags','read write manage'),
        ('relations','read write delete'), ('comments','read write delete'),
        ('attachments','read write delete'), ('knowledge','read write delete'),
        ('journey','read act manage'), ('requirements','read write agree'),
        ('releases','read write deploy'), ('intake','read write decide'),
        ('stage_handoffs','read write decide'), ('harness','read write worker control manage'),
        ('work_orders','read write assign'), ('runs','read write control claim'),
        ('run','create read claim telemetry'), ('account','read manage route probe'),
        ('approvals','read request propose decide decide_high revoke'), ('inbox','read send manage receipt'),
        ('stage','prepare deploy verify apply'), ('chat','read send bind receive'), ('models','read manage resolve'),
        ('plugins','read manage invoke'), ('imports','read manage'),
        ('views','read write share'), ('events','read undo undo_other'),
        ('search','read'), ('hours','read write approve'),
        ('quotes','read write issue accept delete manage portal_read portal_accept'),
        ('crm','read write manage'), ('cost_units','read write manage'),
        ('project_groups','read write'), ('profile','read write manage portal_read portal_write'),
        ('settings','read manage'), ('members','read manage'),
        ('roles','read manage'), ('keys','read manage'),
        ('audit','read'), ('authz','read'), ('ownership','transfer')
      ) AS registry(resource,actions)
      CROSS JOIN LATERAL unnest(string_to_array(registry.actions,' ')) AS a(action)
      WHERE candidate=registry.resource || '.' || a.action
    );
$$;

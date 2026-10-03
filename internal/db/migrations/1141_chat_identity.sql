-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-618 R1. Alias metadata backfill only; no process ownership or delivery activation.
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

-- Native identity is tenant + harness + digest of either registered reference,
-- independent of registration UUID, host, project, principal and generation.
-- Both reference aliases share this namespace; a replacement cannot escape
-- ownership by moving the old reference into the vendor-reference field.
CREATE TABLE chat_native_contexts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    harness text NOT NULL CHECK (harness IN ('claude','codex','pi','cursor','grok')),
    ref_digest bytea NOT NULL CHECK (octet_length(ref_digest)=32),
    role_id uuid NOT NULL,
    owner_person_id uuid NOT NULL,
    project_id uuid NOT NULL,
    PRIMARY KEY (tenant_id,harness,ref_digest),
    UNIQUE (tenant_id,harness,ref_digest,role_id,owner_person_id,project_id),
    FOREIGN KEY (tenant_id,role_id,project_id,owner_person_id) REFERENCES chat_roles(tenant_id,id,project_id,owner_person_id)
);
ALTER TABLE chat_native_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_native_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_native_contexts_tenant ON chat_native_contexts USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_native_contexts_participant ON chat_native_contexts AS RESTRICTIVE USING (
    current_setting('aeon.chat_native_store',true)='on' OR
    owner_person_id=ANY((SELECT aeon_current_principals())::uuid[])
    OR EXISTS(SELECT 1 FROM harness_sessions s WHERE s.tenant_id=chat_native_contexts.tenant_id
      AND s.id=NULLIF(current_setting('aeon.chat_session_id',true),'')::uuid
      AND s.harness=chat_native_contexts.harness
      AND chat_native_contexts.ref_digest IN (s.ref_digest,s.vendor_ref_digest)));

-- Durable undirected alias links survive generation changes and first binding.
-- Edges are immutable: there is deliberately no implicit release operation.
CREATE TABLE chat_native_aliases (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    harness text NOT NULL,
    ref_digest bytea NOT NULL CHECK (octet_length(ref_digest)=32),
    alias_digest bytea NOT NULL CHECK (octet_length(alias_digest)=32),
    PRIMARY KEY (tenant_id,harness,ref_digest,alias_digest),
    CHECK (ref_digest<=alias_digest)
);
CREATE INDEX chat_native_aliases_reverse ON chat_native_aliases(tenant_id,harness,alias_digest,ref_digest);
ALTER TABLE chat_native_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_native_aliases FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_native_aliases_tenant ON chat_native_aliases USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Per-registration history refers to the lasting native-context ownership.
-- Unique keys fence contexts whose previous owner is hidden by RLS.
CREATE TABLE chat_session_contexts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    session_id uuid NOT NULL,
    role_id uuid NOT NULL,
    owner_person_id uuid NOT NULL,
    project_id uuid NOT NULL,
    harness text NOT NULL,
    ref_digest bytea NOT NULL,
    vendor_ref_digest bytea,
    PRIMARY KEY (tenant_id,session_id),
    UNIQUE (tenant_id,session_id,role_id,owner_person_id),
    FOREIGN KEY (tenant_id,project_id,session_id) REFERENCES harness_sessions(tenant_id,project_id,id),
    FOREIGN KEY (tenant_id,role_id,project_id,owner_person_id) REFERENCES chat_roles(tenant_id,id,project_id,owner_person_id),
    FOREIGN KEY (tenant_id,harness,ref_digest,role_id,owner_person_id,project_id) REFERENCES chat_native_contexts(tenant_id,harness,ref_digest,role_id,owner_person_id,project_id),
    FOREIGN KEY (tenant_id,harness,vendor_ref_digest,role_id,owner_person_id,project_id) REFERENCES chat_native_contexts(tenant_id,harness,ref_digest,role_id,owner_person_id,project_id)
);
ALTER TABLE chat_session_bindings ADD CONSTRAINT chat_binding_native_context
    FOREIGN KEY (tenant_id,session_id,role_id,owner_person_id) REFERENCES chat_session_contexts(tenant_id,session_id,role_id,owner_person_id);
ALTER TABLE chat_session_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_session_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_session_contexts_tenant ON chat_session_contexts USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_session_contexts_participant ON chat_session_contexts AS RESTRICTIVE USING (
    owner_person_id=ANY((SELECT aeon_current_principals())::uuid[])
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
CREATE TRIGGER chat_native_contexts_identity BEFORE UPDATE OR DELETE ON chat_native_contexts FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();
CREATE TRIGGER chat_bindings_identity BEFORE UPDATE OR DELETE ON chat_session_bindings FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();

ALTER TABLE chat_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_roles FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_roles_tenant ON chat_roles USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_roles_participant ON chat_roles AS RESTRICTIVE USING (
    owner_person_id=ANY((SELECT aeon_current_principals())::uuid[])
    OR id=NULLIF(current_setting('aeon.chat_role_id',true),'')::uuid);
ALTER TABLE chat_threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_threads FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_threads_tenant ON chat_threads USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_threads_participant ON chat_threads AS RESTRICTIVE USING (
    person_id=ANY((SELECT aeon_current_principals())::uuid[])
    OR role_id=NULLIF(current_setting('aeon.chat_role_id',true),'')::uuid);
ALTER TABLE chat_session_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_session_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY chat_session_bindings_tenant ON chat_session_bindings USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY chat_session_bindings_participant ON chat_session_bindings AS RESTRICTIVE USING (
    owner_person_id=ANY((SELECT aeon_current_principals())::uuid[])
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

-- One ownership store for registration, replay, alias learning, renaming and
-- binding. The caller takes tenant -> hierarchy -> record locks, before events.
-- Trigger entry also takes the tenant fence, so missed application paths fail
-- closed. No SECURITY DEFINER: tenant RLS still applies to this narrow internal
-- visibility scope, including with the production NOBYPASSRLS migration owner.
CREATE FUNCTION aeon_store_chat_native(p_tenant uuid,p_harness text,p_refs bytea[],
    p_person uuid,p_project uuid,p_role uuid DEFAULT NULL) RETURNS void
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE
    refs bytea[];
    component bytea[];
    owner record;
    owned_role uuid := p_role;
    prior_store text := current_setting('aeon.chat_native_store',true);
BEGIN
    IF cardinality(p_refs)>4 THEN
        RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
    END IF;
    -- Attached observation references use 64-byte hex snapshots, not native
    -- session references. They cannot collide with this 32-byte namespace.
    SELECT array_agg(DISTINCT r ORDER BY r) INTO refs FROM unnest(p_refs) r WHERE octet_length(r)=32;
    IF refs IS NULL THEN
        IF p_role IS NOT NULL THEN
            RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
        END IF;
        RETURN;
    END IF;
    -- Never let a native registration escape its graph through an intermediate
    -- observation/malformed encoding, then return with a fresh unlinked digest.
    IF EXISTS(SELECT 1 FROM unnest(p_refs) r WHERE r IS NOT NULL AND octet_length(r)<>32) THEN
        RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
    END IF;
    PERFORM id FROM public.tenants WHERE id=p_tenant FOR NO KEY UPDATE NOWAIT;
    PERFORM set_config('aeon.chat_native_store','on',true);
    INSERT INTO public.chat_native_aliases(tenant_id,harness,ref_digest,alias_digest)
        SELECT p_tenant,p_harness,refs[1],r FROM unnest(refs) r ON CONFLICT DO NOTHING;
    -- UNION deduplicates cycles; LIMIT bounds traversal before aggregation.
    -- A component beyond this bound is refused, never partially claimed.
    WITH RECURSIVE connected(ref) AS (
        SELECT unnest(refs)
        UNION
        SELECT CASE WHEN a.ref_digest=c.ref THEN a.alias_digest ELSE a.ref_digest END
        FROM connected c JOIN public.chat_native_aliases a ON
            a.tenant_id=p_tenant AND a.harness=p_harness AND (a.ref_digest=c.ref OR a.alias_digest=c.ref)
    ) SELECT array_agg(ref) INTO component FROM (SELECT ref FROM connected LIMIT 1025) bounded;
    IF cardinality(component)>1024 THEN
        RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
    END IF;
    FOR owner IN SELECT DISTINCT role_id,owner_person_id,project_id FROM public.chat_native_contexts
        WHERE tenant_id=p_tenant AND harness=p_harness AND ref_digest=ANY(component)
    LOOP
        IF owner.owner_person_id IS DISTINCT FROM p_person OR owner.project_id IS DISTINCT FROM p_project
            OR (owned_role IS NOT NULL AND owned_role<>owner.role_id) THEN
            RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
        END IF;
        owned_role := owner.role_id;
    END LOOP;
    IF owned_role IS NOT NULL THEN
        IF p_person IS NULL OR p_project IS NULL THEN
            RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
        END IF;
        INSERT INTO public.chat_native_contexts(tenant_id,harness,ref_digest,role_id,owner_person_id,project_id)
            SELECT p_tenant,p_harness,r,owned_role,p_person,p_project FROM unnest(component) r ORDER BY r
            ON CONFLICT (tenant_id,harness,ref_digest) DO NOTHING;
    END IF;
    PERFORM set_config('aeon.chat_native_store',coalesce(prior_store,''),true);
END;
$$;

-- Raw claims cannot skip graph propagation; only the store writes these rows.
CREATE FUNCTION aeon_chat_native_store_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('aeon.chat_native_store',true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER chat_native_claim_store BEFORE INSERT ON chat_native_contexts FOR EACH ROW EXECUTE FUNCTION aeon_chat_native_store_only();
CREATE TRIGGER chat_native_alias_store BEFORE INSERT ON chat_native_aliases FOR EACH ROW EXECUTE FUNCTION aeon_chat_native_store_only();
CREATE TRIGGER chat_native_alias_immutable BEFORE UPDATE OR DELETE ON chat_native_aliases FOR EACH ROW EXECUTE FUNCTION aeon_chat_identity_immutable();

CREATE FUNCTION aeon_chat_registration_store() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE refs bytea[] := ARRAY[NEW.ref_digest,NEW.vendor_ref_digest];
BEGIN
    IF TG_OP='UPDATE' THEN
        IF NEW.tenant_id<>OLD.tenant_id OR NEW.harness<>OLD.harness THEN
            RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
        END IF;
        refs := refs || ARRAY[OLD.ref_digest,OLD.vendor_ref_digest];
    END IF;
    PERFORM aeon_store_chat_native(NEW.tenant_id,NEW.harness,refs,NEW.owner_principal_id,NEW.project_id);
    RETURN NEW;
END;
$$;
CREATE TRIGGER chat_registration_store BEFORE INSERT OR UPDATE OF tenant_id,harness,ref_digest,vendor_ref_digest,owner_principal_id,project_id ON harness_sessions
    FOR EACH ROW EXECUTE FUNCTION aeon_chat_registration_store();

CREATE FUNCTION aeon_chat_context_store() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s record;
BEGIN
    SELECT * INTO s FROM harness_sessions WHERE tenant_id=NEW.tenant_id AND id=NEW.session_id;
    IF NOT FOUND OR s.owner_principal_id IS DISTINCT FROM NEW.owner_person_id OR s.project_id<>NEW.project_id
        OR s.harness<>NEW.harness OR s.ref_digest<>NEW.ref_digest OR s.vendor_ref_digest IS DISTINCT FROM NEW.vendor_ref_digest THEN
        RAISE EXCEPTION 'chat binding unavailable' USING ERRCODE='23514',CONSTRAINT='chat_native_owner';
    END IF;
    PERFORM aeon_store_chat_native(NEW.tenant_id,NEW.harness,ARRAY[NEW.ref_digest,NEW.vendor_ref_digest],NEW.owner_person_id,NEW.project_id,NEW.role_id);
    RETURN NEW;
END;
$$;
CREATE TRIGGER chat_context_store BEFORE INSERT ON chat_session_contexts FOR EACH ROW EXECUTE FUNCTION aeon_chat_context_store();

-- Seed relationships of pre-chat registrations too; no owners or deliveries
-- are invented. Tenant RLS remains enabled, and all temporary scope is restored.
CREATE FUNCTION aeon_seed_chat_native_aliases() RETURNS void LANGUAGE plpgsql AS $$
DECLARE t record;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_projects text := current_setting('aeon.visible_projects',true);
    prior_store text := current_setting('aeon.chat_native_store',true);
BEGIN
    PERFORM set_config('aeon.visible_projects','*',true);
    PERFORM set_config('aeon.chat_native_store','on',true);
    FOR t IN SELECT id FROM tenants LOOP
        PERFORM set_config('aeon.tenant_id',t.id::text,true);
        INSERT INTO chat_native_aliases(tenant_id,harness,ref_digest,alias_digest)
            SELECT tenant_id,harness,least(ref_digest,coalesce(vendor_ref_digest,ref_digest)),greatest(ref_digest,coalesce(vendor_ref_digest,ref_digest))
            FROM harness_sessions WHERE tenant_id=t.id AND octet_length(ref_digest)=32
                AND (vendor_ref_digest IS NULL OR octet_length(vendor_ref_digest)=32) ON CONFLICT DO NOTHING;
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.visible_projects',coalesce(prior_projects,''),true);
    PERFORM set_config('aeon.chat_native_store',coalesce(prior_store,''),true);
END;
$$;
SELECT aeon_seed_chat_native_aliases();

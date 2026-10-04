-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-641 fix2: preserve physical ownership/choices through link and unlink.
-- Reserved by LEAD in the 1206–1209 themes range. Existing rows/audit are not rewritten.
SET LOCAL lock_timeout = '5s';

-- Resolve from the authenticated principal (first array entry), not the cached
-- canonical entry: topology may have changed while a write waited on its fence.
-- Person-only, tenant-scoped and live; agents never inherit creator identities.
CREATE FUNCTION aeon_theme_owns(person uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM principals caller
        JOIN principals canonical ON canonical.tenant_id=caller.tenant_id
            AND canonical.id=coalesce(caller.linked_to,caller.id)
        JOIN principals owner ON owner.tenant_id=caller.tenant_id AND owner.id=person
            AND coalesce(owner.linked_to,owner.id)=canonical.id
        WHERE caller.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
            AND caller.id=(aeon_current_principals())[1]
            AND caller.kind='person' AND caller.status='active'
            AND canonical.kind='person' AND canonical.status='active'
            AND owner.kind='person' AND owner.status='active')
$$;

ALTER POLICY themes_owner ON themes
    USING (scope<>'personal' OR (SELECT aeon_visibility_system())
        OR aeon_theme_owns(owner_principal_id))
    WITH CHECK (scope<>'personal' OR (SELECT aeon_visibility_system())
        OR aeon_theme_owns(owner_principal_id));
ALTER POLICY theme_selections_owner ON theme_selections
    USING ((SELECT aeon_visibility_system()) OR aeon_theme_owns(principal_id))
    WITH CHECK ((SELECT aeon_visibility_system()) OR aeon_theme_owns(principal_id));
ALTER POLICY events_theme_audience ON events
    USING (type NOT LIKE 'theme.%' OR (SELECT aeon_visibility_system())
        OR coalesce(after->>'scope',before->>'scope') IN ('default','workspace')
        OR aeon_theme_owns(aeon_uuid_or_null(metadata->>'audience_principal_id')));

-- Keep owner identity immutable. Existing owners may become aliases; only
-- newly created themes require a canonical owner. Choice rows remain attached
-- to their original person; visibility of a chosen theme follows current links.
CREATE OR REPLACE FUNCTION aeon_theme_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        IF OLD.scope='default' THEN
            RAISE EXCEPTION 'workspace default cannot be deleted' USING ERRCODE='23514';
        END IF;
        RETURN OLD;
    END IF;
    IF TG_OP='UPDATE' AND (NEW.tenant_id,NEW.id,NEW.scope,NEW.owner_principal_id,NEW.created_at)
        IS DISTINCT FROM (OLD.tenant_id,OLD.id,OLD.scope,OLD.owner_principal_id,OLD.created_at) THEN
        RAISE EXCEPTION 'theme identity, scope and ownership are immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.scope='personal' AND NOT EXISTS (SELECT 1 FROM principals
        WHERE tenant_id=NEW.tenant_id AND id=NEW.owner_principal_id AND kind='person' AND (TG_OP='UPDATE' OR linked_to IS NULL)) THEN
        RAISE EXCEPTION 'personal theme owner must be a person (canonical at creation)' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION aeon_theme_selection_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM principals WHERE tenant_id=NEW.tenant_id
        AND id=NEW.principal_id AND kind='person') THEN
        RAISE EXCEPTION 'theme choice belongs to a person' USING ERRCODE='23514';
    END IF;
    -- A tombstone is valid for fallback and undo, but a different person's
    -- theme is never selectable, even by a direct SQL or system writer.
    IF NEW.theme_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM themes
        WHERE tenant_id=NEW.tenant_id AND id=NEW.theme_id
        AND (scope<>'personal' OR (SELECT coalesce(linked_to,id) FROM principals
            WHERE tenant_id=NEW.tenant_id AND id=owner_principal_id) =
            (SELECT coalesce(linked_to,id) FROM principals
            WHERE tenant_id=NEW.tenant_id AND id=NEW.principal_id))) THEN
        RAISE EXCEPTION 'theme choice must be visible to its owner' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

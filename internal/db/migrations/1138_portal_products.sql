-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-619. Additive product registry; nodes, legacy ballots and URLs survive.
SET LOCAL lock_timeout = '5s';

CREATE TABLE portal_products (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    product_id uuid NOT NULL,
    slug text NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{0,62}$'),
    published boolean NOT NULL DEFAULT false,
    is_default boolean NOT NULL DEFAULT false,
    participation_policy text NOT NULL DEFAULT 'disabled'
        CHECK (participation_policy IN ('disabled', 'legacy', 'registered')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    theme_revision bigint NOT NULL DEFAULT 0 CHECK (theme_revision >= 0),
    registered_since timestamptz,
    anonymous_history_until timestamptz,
    PRIMARY KEY (tenant_id, product_id),
    UNIQUE (tenant_id, slug),
    FOREIGN KEY (tenant_id, product_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX portal_products_default ON portal_products(tenant_id) WHERE is_default;
ALTER TABLE portal_products ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_products FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_products_tenant ON portal_products
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- New links are product-scoped. The old tenant link remains usable by the
-- previous binary; its trigger mirrors changes to its explicitly bound product.
CREATE TABLE portal_product_pace (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    product_id uuid NOT NULL,
    project_node_id uuid NOT NULL,
    release_history boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL CHECK (revision > 0),
    PRIMARY KEY (tenant_id, product_id),
    FOREIGN KEY (tenant_id, product_id) REFERENCES portal_products(tenant_id, product_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE portal_product_pace ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_product_pace FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_product_pace_tenant ON portal_product_pace
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE portal_pace ADD COLUMN product_id uuid;
ALTER TABLE portal_pace ADD FOREIGN KEY (tenant_id, product_id)
    REFERENCES portal_products(tenant_id, product_id) ON DELETE CASCADE;
ALTER TABLE portal_votes ADD COLUMN product_id uuid;
ALTER TABLE portal_votes ADD FOREIGN KEY (tenant_id, product_id)
    REFERENCES portal_products(tenant_id, product_id) ON DELETE CASCADE;
CREATE INDEX portal_votes_product ON portal_votes(tenant_id, product_id, wish_id);

-- FORCE RLS applies to the migration owner too. Preserve the old choice once,
-- including its position/key tie-break, then never derive a default from order.
DO $$
DECLARE t record; prior text := current_setting('aeon.tenant_id', true);
BEGIN
    FOR t IN SELECT id FROM tenants LOOP
        PERFORM set_config('aeon.tenant_id', t.id::text, true);
        PERFORM set_config('aeon.portal_moderation','on',true);
        INSERT INTO portal_products(tenant_id, product_id, slug, published, is_default, participation_policy)
        SELECT tenant_id, id, lower(key), state='published', rank=1,
               CASE WHEN state='published' THEN 'legacy' ELSE 'disabled' END
        FROM (
            SELECT n.*, row_number() OVER (ORDER BY (n.state='published') DESC, n.position, n.key, n.id) AS rank
            FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
            WHERE k.slug='portal_product' AND n.parent_id IS NULL AND n.deleted_at IS NULL
        ) products;
        UPDATE portal_pace SET product_id=(SELECT product_id FROM portal_products WHERE is_default);
        INSERT INTO portal_product_pace(tenant_id, product_id, project_node_id, release_history, revision)
        SELECT tenant_id, product_id, project_node_id, release_history, revision FROM portal_pace WHERE product_id IS NOT NULL;
        UPDATE portal_votes v SET product_id=p.product_id
        FROM nodes w JOIN portal_products p ON p.tenant_id=w.tenant_id AND p.product_id=w.parent_id
        WHERE v.tenant_id=w.tenant_id AND v.wish_id=w.id;
    END LOOP;
    PERFORM set_config('aeon.tenant_id', coalesce(prior, ''), true);
END;
$$;

-- This is a code activation gate, never a user-editable setting or env flag.
-- B3+B8+B7 must replace it through their reviewed activation migration.
CREATE FUNCTION aeon_portal_registered_ready() RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT false $$;

CREATE FUNCTION aeon_portal_product_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' AND pg_trigger_depth()>1 THEN RETURN OLD; END IF;
    IF TG_OP='INSERT' AND pg_trigger_depth()>1 AND NOT NEW.published
       AND NEW.participation_policy='disabled' THEN RETURN NEW; END IF;
    IF current_setting('aeon.portal_moderation', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'portal moderation required' USING ERRCODE='42501';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    IF NEW.participation_policy='registered' AND NOT aeon_portal_registered_ready() THEN
        RAISE EXCEPTION 'registered participation not ready' USING ERRCODE='23514';
    END IF;
    IF TG_OP='UPDATE' AND NEW.participation_policy='registered' AND OLD.participation_policy<>'registered' THEN
        NEW.registered_since := clock_timestamp();
        NEW.anonymous_history_until := NEW.registered_since;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER portal_products_guard BEFORE INSERT OR UPDATE OR DELETE ON portal_products
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_product_guard();
CREATE FUNCTION aeon_portal_product_pace_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' AND pg_trigger_depth()>1 THEN RETURN OLD; END IF;
    IF current_setting('aeon.portal_moderation',true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'portal moderation required' USING ERRCODE='42501';
    END IF;
    RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE TRIGGER portal_product_pace_guard BEFORE INSERT OR UPDATE OR DELETE ON portal_product_pace
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_product_pace_guard();

CREATE FUNCTION aeon_portal_register_product() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_id IS NULL AND EXISTS (SELECT 1 FROM node_kinds k
       WHERE k.tenant_id=NEW.tenant_id AND k.id=NEW.kind_id AND k.slug='portal_product') THEN
        INSERT INTO portal_products(tenant_id, product_id, slug, is_default)
        VALUES(NEW.tenant_id, NEW.id, lower(NEW.key),
               NOT EXISTS(SELECT 1 FROM portal_products WHERE is_default));
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER nodes_portal_register AFTER INSERT ON nodes
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_register_product();

CREATE FUNCTION aeon_portal_bind_legacy_pace() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.product_id := coalesce(NEW.product_id, (SELECT product_id FROM portal_products WHERE is_default));
    RETURN NEW;
END;
$$;
CREATE TRIGGER portal_pace_bind BEFORE INSERT OR UPDATE ON portal_pace
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_bind_legacy_pace();
CREATE FUNCTION aeon_portal_mirror_legacy_pace() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        DELETE FROM portal_product_pace WHERE tenant_id=OLD.tenant_id AND product_id=OLD.product_id;
        RETURN OLD;
    END IF;
    IF NEW.product_id IS NOT NULL THEN
        INSERT INTO portal_product_pace(tenant_id,product_id,project_node_id,release_history,revision)
        VALUES(NEW.tenant_id,NEW.product_id,NEW.project_node_id,NEW.release_history,NEW.revision)
        ON CONFLICT(tenant_id,product_id) DO UPDATE
        SET project_node_id=EXCLUDED.project_node_id, release_history=EXCLUDED.release_history, revision=EXCLUDED.revision;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER portal_pace_mirror AFTER INSERT OR UPDATE OR DELETE ON portal_pace
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_mirror_legacy_pace();

-- Previous-binary ballots also acquire a stable product binding. Null legacy
-- orphan bindings are retained as history and never appear in active sums.
CREATE FUNCTION aeon_portal_bind_ballot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.product_id := coalesce(NEW.product_id, (SELECT parent_id FROM nodes WHERE tenant_id=NEW.tenant_id AND id=NEW.wish_id));
    RETURN NEW;
END;
$$;
CREATE TRIGGER portal_votes_bind BEFORE INSERT ON portal_votes
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_bind_ballot();

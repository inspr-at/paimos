-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. Portal catalog rows are a workspace decision. The event guard only
-- sees ids named on the event, so two indirect writes slipped through: sibling
-- renumbering (numeric rank runs out and applyPositions rewrites every sibling,
-- including a portal product's position) and the project cascade (moving an
-- ordinary ancestor copies project_id onto a descendant portal wish).
--
-- This trigger sees the row itself. Position-only renumbering is not exempt:
-- the public catalog orders products by position, so a position write is a
-- publication change. A move that would rewrite one fails and rolls back.
-- A person with settings.manage arms the transaction first
-- (set_config('aeon.portal_moderation','on',true)); every other update or
-- delete of a portal_product, portal_feature or portal_wish row is refused.
-- Inserts stay open so public wish intake can still append a pending wish.

CREATE FUNCTION aeon_portal_row_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    old_slug text;
    new_slug text;
BEGIN
    IF current_setting('aeon.portal_moderation', true) = 'on' THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    SELECT k.slug INTO old_slug FROM node_kinds k
     WHERE k.tenant_id = OLD.tenant_id AND k.id = OLD.kind_id;
    IF TG_OP = 'DELETE' THEN
        IF old_slug IN ('portal_product', 'portal_feature', 'portal_wish') THEN
            RAISE EXCEPTION 'portal moderation required' USING ERRCODE = '42501';
        END IF;
        RETURN OLD;
    END IF;

    new_slug := old_slug;
    IF NEW.kind_id IS DISTINCT FROM OLD.kind_id THEN
        SELECT k.slug INTO new_slug FROM node_kinds k
         WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.kind_id;
    END IF;
    IF old_slug IN ('portal_product', 'portal_feature', 'portal_wish')
       OR new_slug IN ('portal_product', 'portal_feature', 'portal_wish') THEN
        RAISE EXCEPTION 'portal moderation required' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER nodes_portal_row_guard
    BEFORE UPDATE OR DELETE ON nodes
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_row_guard();

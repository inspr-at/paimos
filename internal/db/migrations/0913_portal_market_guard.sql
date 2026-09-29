-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-125. Publication and moderation writes on the market tables need the
-- same transaction-local flag as the portal node guard. A person with
-- settings.manage arms it (set_config('aeon.portal_moderation','on',true)).
-- Anonymous intake may still insert a pending correction. A cascaded delete
-- from a node the caller is already allowed to remove is not a second decision.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_portal_market_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('aeon.portal_moderation', true) = 'on' THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    -- Foreign-key cascades nest this trigger inside the node delete.
    -- Field reads stay inside a table branch: one function serves every table,
    -- and a missing column errors even when an AND would not use it.
    IF TG_OP = 'DELETE' AND pg_trigger_depth() > 1 THEN
        IF TG_TABLE_NAME = 'portal_pace'
           OR TG_TABLE_NAME = 'portal_fulfillments'
           OR TG_TABLE_NAME = 'portal_cell_revisions' THEN
            RETURN OLD;
        END IF;
    END IF;

    IF TG_TABLE_NAME = 'portal_corrections' THEN
        IF TG_OP = 'INSERT' AND NEW.state = 'pending' THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'portal moderation required' USING ERRCODE = '42501';
    END IF;

    IF TG_TABLE_NAME = 'portal_cells' THEN
        IF TG_OP = 'DELETE' THEN
            IF NOT OLD.approved THEN
                RETURN OLD;
            END IF;
        ELSIF TG_OP = 'INSERT' THEN
            IF NOT NEW.approved THEN
                RETURN NEW;
            END IF;
        ELSIF NOT OLD.approved AND NOT NEW.approved THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'portal moderation required' USING ERRCODE = '42501';
    END IF;

    IF TG_TABLE_NAME = 'portal_competitors' THEN
        IF TG_OP = 'DELETE' THEN
            IF NOT OLD.published THEN
                RETURN OLD;
            END IF;
        ELSIF TG_OP = 'INSERT' THEN
            IF NOT NEW.published THEN
                RETURN NEW;
            END IF;
        ELSIF NOT OLD.published AND NOT NEW.published THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'portal moderation required' USING ERRCODE = '42501';
    END IF;

    RAISE EXCEPTION 'portal moderation required' USING ERRCODE = '42501';
END;
$$;

CREATE TRIGGER portal_competitors_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_competitors
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

CREATE TRIGGER portal_aspects_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_aspects
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

CREATE TRIGGER portal_cells_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_cells
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

CREATE TRIGGER portal_cell_revisions_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_cell_revisions
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

CREATE TRIGGER portal_corrections_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_corrections
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

CREATE TRIGGER portal_pace_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_pace
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

CREATE TRIGGER portal_fulfillments_moderation
    BEFORE INSERT OR UPDATE OR DELETE ON portal_fulfillments
    FOR EACH ROW EXECUTE FUNCTION aeon_portal_market_guard();

COMMENT ON FUNCTION aeon_portal_market_guard() IS
    'Refuses publication and moderation writes unless aeon.portal_moderation is on for this transaction.';

-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-641 fix3: unique public CAS generations survive winning-identity changes.
-- LEAD reserved 1206–1209 for AEON-641. Physical revisions and audit stay intact.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- Keep API integers exactly representable by JavaScript. NO CYCLE makes
-- exhaustion fail closed rather than reusing a generation. Gaps are expected.
CREATE SEQUENCE theme_selection_generation AS bigint
    MINVALUE 1 MAXVALUE 9007199254740991 NO CYCLE;
-- Reject cached physical revisions from before this migration too: allocate
-- every new generation above all previously issued physical revisions. Fence
-- legacy writers during that scan; FORCE RLS requires visiting each tenant.
LOCK TABLE theme_selections IN ACCESS EXCLUSIVE MODE;
DO $$
DECLARE
    target uuid;
    greatest_revision bigint := 0;
    tenant_revision bigint;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_system text := current_setting('aeon.system',true);
BEGIN
    PERFORM set_config('aeon.system','on',true);
    FOR target IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id',target::text,true);
        SELECT coalesce(max(revision),0) INTO tenant_revision FROM theme_selections;
        greatest_revision := greatest(greatest_revision,tenant_revision);
    END LOOP;
    PERFORM setval('theme_selection_generation',greatest_revision+1,false);
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.system',coalesce(prior_system,''),true);
END;
$$;
ALTER TABLE theme_selections ADD COLUMN generation bigint NOT NULL
    DEFAULT nextval('theme_selection_generation');
ALTER SEQUENCE theme_selection_generation OWNED BY theme_selections.generation;

-- The trigger also covers previous binaries and direct SQL writers. Never
-- restore an old generation during undo or depend on a caller incrementing it.
CREATE FUNCTION aeon_theme_selection_generation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.generation := nextval('theme_selection_generation');
    RETURN NEW;
END;
$$;
CREATE TRIGGER theme_selections_generation BEFORE UPDATE ON theme_selections
    FOR EACH ROW EXECUTE FUNCTION aeon_theme_selection_generation();

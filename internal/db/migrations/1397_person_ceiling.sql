-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1591: additive person ceiling. The previous three-argument entry stays,
-- so an older binary keeps calling it. NULL acting person is that legacy path.
-- A present id that is missing, not a person, or inactive yields no content.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_person_visibility(p_tenant uuid, p_person uuid) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM principals p
        WHERE p.tenant_id = p_tenant AND p.id = p_person AND p.kind = 'person')
        THEN aeon_principal_visibility(p_tenant, p_person) ELSE '' END
$$;

CREATE FUNCTION aeon_enter_principal(p_tenant uuid, p_principal uuid, p_key_creator uuid, p_acting_person uuid) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    visibility text;
BEGIN
    visibility := aeon_enter_principal(p_tenant, p_principal, p_key_creator);
    IF p_acting_person IS NOT NULL THEN
        visibility := aeon_visibility_intersect(visibility, aeon_person_visibility(p_tenant, p_acting_person));
        PERFORM set_config('aeon.visible_projects', visibility, true);
    END IF;
    RETURN visibility;
END
$$;

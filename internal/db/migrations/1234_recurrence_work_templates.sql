-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-653, reserved 1234. Only stored kind references change; occurrence
-- receipts, historical sessions and Decision Desk identities are untouched.
SET LOCAL lock_timeout = '5s';
DO $$
DECLARE t record; prior_tenant text := current_setting('aeon.tenant_id',true); prior_visible text := current_setting('aeon.visible_projects',true);
BEGIN
 FOR t IN SELECT id FROM tenants ORDER BY id LOOP
  PERFORM set_config('aeon.tenant_id',t.id::text,true),set_config('aeon.visible_projects','*',true);
  UPDATE recurrences SET template=jsonb_set(template,'{type}','"work"'::jsonb),revision=revision+1
   WHERE tenant_id=t.id AND template->>'type' IN ('epic','ticket','task');
 END LOOP;
 PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true),set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
END;
$$;

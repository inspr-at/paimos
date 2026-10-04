-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-521A/B share this nullable pending check. IF NOT EXISTS lets each
-- package measure CI independently; 1078 retains the same column contract.
SET LOCAL lock_timeout = '5s';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS human_check text;

CREATE FUNCTION aeon_human_check_properties() RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
SELECT '{"human_check_completed":{"type":["object","null"],"description":"Server-written person check: text, by and at.","properties":{"text":{"type":"string"},"by":{"type":"string"},"at":{"type":"string"}},"required":["text","by","at"],"additionalProperties":false}}'::jsonb;
$$;
CREATE FUNCTION aeon_extend_human_check_schema() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.slug IN ('ticket','task') THEN
  NEW.field_schema := jsonb_set(NEW.field_schema, '{properties}',
   coalesce(NEW.field_schema->'properties','{}'::jsonb) || aeon_human_check_properties());
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER human_check_schema BEFORE INSERT OR UPDATE ON node_kinds
 FOR EACH ROW EXECUTE FUNCTION aeon_extend_human_check_schema();
UPDATE node_kinds SET field_schema=jsonb_set(field_schema, '{properties}',
 coalesce(field_schema->'properties','{}'::jsonb) || aeon_human_check_properties())
 WHERE slug IN ('ticket','task');

-- SPDX-License-Identifier: AGPL-3.0-only
-- Existing keys retain their legacy exemption until adopted. New keys never do.
ALTER TABLE agent_keys ADD COLUMN person_owner_required boolean NOT NULL DEFAULT false;
ALTER TABLE agent_keys ALTER COLUMN person_owner_required SET DEFAULT true;
ALTER TABLE agent_keys ADD CONSTRAINT agent_key_person_owner_required
 CHECK (NOT person_owner_required OR created_by_principal_id IS NOT NULL);

CREATE FUNCTION agent_key_person_owner_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  NEW.person_owner_required := true;
 ELSIF (OLD.person_owner_required AND NOT NEW.person_owner_required) OR (OLD.created_by_principal_id IS NOT NULL AND NEW.created_by_principal_id IS NULL) THEN
  RAISE EXCEPTION 'person ownership cannot be cleared' USING ERRCODE='23514';
 END IF;
 IF NEW.created_by_principal_id IS NOT NULL THEN
  IF NOT EXISTS(SELECT 1 FROM principals WHERE tenant_id=NEW.tenant_id AND id=NEW.created_by_principal_id AND kind='person') THEN
   RAISE EXCEPTION 'agent key creator must be a person in the same tenant' USING ERRCODE='23514';
  END IF;
  NEW.person_owner_required := true;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER agent_key_person_owner_guard BEFORE INSERT OR UPDATE OF created_by_principal_id,person_owner_required ON agent_keys
 FOR EACH ROW EXECUTE FUNCTION agent_key_person_owner_guard();

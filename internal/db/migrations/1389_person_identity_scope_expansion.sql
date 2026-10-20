-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1585: expansion only. Old writers keep producing valid workspace/project
-- rows with NULL identity/computer columns. No backfill or actor inference.
-- Replacing CHECKs with supersets needs an exact-byte migration-policy exception.
SET LOCAL lock_timeout = '5s';

ALTER TABLE agent_keys ADD COLUMN person_id uuid;
ALTER TABLE agent_keys ADD FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id);
ALTER TABLE agent_runs ADD COLUMN acting_person_id uuid;
ALTER TABLE agent_runs ADD FOREIGN KEY (tenant_id,acting_person_id) REFERENCES principals(tenant_id,id);
ALTER TABLE role_bindings ADD COLUMN computer_id uuid;
ALTER TABLE role_bindings ADD FOREIGN KEY (tenant_id,computer_id) REFERENCES agent_pairing_computers(tenant_id,id);

-- Never assume PostgreSQL's generated names. Match the CHECKs by their actual
-- column dependencies, and abort rather than removing an unfamiliar constraint.
DO $$
DECLARE
    type_column smallint;
    scope_column smallint;
    type_check text;
    shape_check text;
BEGIN
    SELECT attnum INTO STRICT type_column FROM pg_attribute
      WHERE attrelid='role_bindings'::regclass AND attname='scope_type' AND NOT attisdropped;
    SELECT attnum INTO STRICT scope_column FROM pg_attribute
      WHERE attrelid='role_bindings'::regclass AND attname='scope_id' AND NOT attisdropped;
    SELECT conname INTO STRICT type_check FROM pg_constraint
      WHERE conrelid='role_bindings'::regclass AND contype='c'
        AND conkey=ARRAY[type_column]::smallint[];
    SELECT conname INTO STRICT shape_check FROM pg_constraint
      WHERE conrelid='role_bindings'::regclass AND contype='c'
        AND conkey @> ARRAY[type_column,scope_column]::smallint[] AND cardinality(conkey)=2;
    RAISE NOTICE 'AEON-1585 scope constraints: %, %',type_check,shape_check;
    EXECUTE format('ALTER TABLE role_bindings DROP CONSTRAINT %I',type_check);
    EXECUTE format('ALTER TABLE role_bindings DROP CONSTRAINT %I',shape_check);
END;
$$;

ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_scope_type_expanded
  CHECK (scope_type IN ('workspace','project','computer','node'));
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_scope_shape_expanded CHECK (
  (scope_type='workspace' AND scope_id IS NULL AND computer_id IS NULL) OR
  (scope_type IN ('project','node') AND scope_id IS NOT NULL AND computer_id IS NULL) OR
  (scope_type='computer' AND computer_id IS NOT NULL AND scope_id IS NULL)
);
-- Existing partial indexes and ON CONFLICT inference remain unchanged.
CREATE UNIQUE INDEX role_bindings_computer ON role_bindings(tenant_id,principal_id,computer_id)
  WHERE scope_type='computer';
CREATE UNIQUE INDEX role_bindings_node ON role_bindings(tenant_id,principal_id,scope_id)
  WHERE scope_type='node';

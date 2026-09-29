-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-309: INSPR-CalVer3. New releases declare inspr-calver-3; every row that
-- already declares inspr-calendar-v2 (or v1, legacy) stays valid and untouched.
-- Additive only: each version_scheme check gains inspr-calver-3.
SET LOCAL lock_timeout = '5s';

DO $$
DECLARE
    tbl text;
    old_name text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['journey_releases', 'stage_handoff_evidence', 'stage_handoff_build_evidence'] LOOP
        -- Idempotent: a table whose check already admits inspr-calver-3 is left alone.
        IF EXISTS (
            SELECT 1 FROM pg_constraint con
            WHERE con.conrelid = tbl::regclass
              AND con.contype = 'c'
              AND pg_get_constraintdef(con.oid) ILIKE '%version_scheme%inspr-calver-3%'
        ) THEN
            CONTINUE;
        END IF;
        SELECT con.conname INTO old_name
        FROM pg_constraint con
        WHERE con.conrelid = tbl::regclass
          AND con.contype = 'c'
          AND pg_get_constraintdef(con.oid) ILIKE '%version_scheme%inspr-calendar-v2%';
        IF old_name IS NULL THEN
            RAISE EXCEPTION '% version_scheme check not found', tbl;
        END IF;
        EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', tbl, old_name);
        EXECUTE format(
            'ALTER TABLE %I ADD CONSTRAINT %I CHECK (version_scheme IN (%L, %L, %L, %L))',
            tbl, tbl || '_version_scheme_check', 'legacy', 'inspr-calendar-v1', 'inspr-calendar-v2', 'inspr-calver-3');
    END LOOP;
END $$;

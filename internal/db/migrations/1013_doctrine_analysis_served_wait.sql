-- SPDX-License-Identifier: AGPL-3.0-only
-- A merged proposal waiting for instruction provenance is no longer open.
SET LOCAL lock_timeout = '5s';
ALTER TABLE doctrine_findings DROP CONSTRAINT doctrine_findings_status_check;
ALTER TABLE doctrine_findings ADD CONSTRAINT doctrine_findings_status_check
 CHECK (status IN ('pending','draft','awaiting_use','internal_note','observed','closed'));
-- Like doctrine_proposals, history retains the source identity after a person
-- removes that source. Analysis history must not block the existing delete API.
ALTER TABLE doctrine_findings DROP CONSTRAINT doctrine_findings_tenant_id_source_id_fkey;

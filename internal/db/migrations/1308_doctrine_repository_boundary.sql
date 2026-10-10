-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1043: widen the two released repository checks to valid GitHub names.
-- Every name accepted by 1024 remains accepted. Both replacements run in one
-- transaction, preserving the existing rows, columns and tenant RLS policies.
-- Proposal authority is the validated host-owned pair, checked by the App and
-- every proposal path; a syntactically valid name alone grants no authority.
-- DROP CONSTRAINT requires the accompanying exact-byte policy review artifact
-- and coordinator/previous-binary compatibility gates before merge or release.
SET LOCAL lock_timeout = '5s';
ALTER TABLE doctrine_proposals DROP CONSTRAINT doctrine_proposals_repository_check;
ALTER TABLE doctrine_proposals ADD CONSTRAINT doctrine_proposals_repository_check
 CHECK(repository ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$'
   AND split_part(repository, '/', 2) NOT IN ('.', '..'));
ALTER TABLE doctrine_machine_pins DROP CONSTRAINT doctrine_machine_pins_repository_check;
ALTER TABLE doctrine_machine_pins ADD CONSTRAINT doctrine_machine_pins_repository_check
 CHECK(repository ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$'
   AND split_part(repository, '/', 2) NOT IN ('.', '..'));

-- SPDX-License-Identifier: AGPL-3.0-only
-- Reuse the tenant-isolated approval table; a lease never contains a transcript.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_attach_requests DROP CONSTRAINT harness_attach_requests_state_check;
ALTER TABLE harness_attach_requests ADD CONSTRAINT harness_attach_requests_state_check
 CHECK (state IN ('pending','approved','active','detached','unreachable','confirmed_exited'));
ALTER TABLE harness_attach_requests ADD CONSTRAINT harness_attach_lease_content_free
 CHECK ((NOT (snapshot ? 'mode') OR
   (snapshot->>'mode' = 'lease' AND snapshot->>'transcript' = '' AND snapshot->>'file_id' = '')) IS TRUE);

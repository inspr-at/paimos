-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1022: the release path gains two recorded steps, the exact-SHA
-- rehearsal and the full test catalogue (Arion v5 §4b). The step_key check is
-- replaced by its strict superset in one transaction: every key it accepted
-- before is still accepted, so a previous binary that writes only the old keys
-- is never refused. DROP CONSTRAINT is outside the expand-safe allowlist; this
-- file carries an exact-byte policy exception.
SET LOCAL lock_timeout = '5s';
ALTER TABLE delivery_flow_steps DROP CONSTRAINT delivery_flow_steps_step_key_check;
ALTER TABLE delivery_flow_steps ADD CONSTRAINT delivery_flow_steps_step_key_check
 CHECK(step_key IN ('a','b','c','d','e','f','g','h','i','j','k','l','copy_gate','pin_gate','build','review','ci','queue','merge_round','hold','mitigation','switch','live_check','rehearsal','catalogue'));

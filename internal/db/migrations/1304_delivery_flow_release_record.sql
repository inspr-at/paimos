-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1022 expansion: the release record facts the Flow shows beside a
-- release run (Arion v5 WP1.10): the native-qualification evidence reference
-- and the rollback class. Nullable columns only; older binaries never write
-- them and read the rows unchanged.
SET LOCAL lock_timeout = '5s';
ALTER TABLE delivery_flow_items
 ADD COLUMN qualification_evidence text CHECK(length(qualification_evidence) BETWEEN 1 AND 200),
 ADD COLUMN rollback_class text CHECK(rollback_class IN ('digest_safe','restore_required'));

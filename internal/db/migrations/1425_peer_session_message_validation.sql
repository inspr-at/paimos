-- SPDX-License-Identifier: AGPL-3.0-only
-- Release 1424's exclusive DDL locks before scanning historical rows. These
-- constraints are strict supersets of the released, validated predicates, so
-- every old writer and row remains valid. Validation permits normal writes.
ALTER TABLE inbox_messages VALIDATE CONSTRAINT inbox_messages_distinct_participants;
ALTER TABLE inbox_compat_messages VALIDATE CONSTRAINT inbox_compat_sender_session_fk;
ALTER TABLE inbox_compat_messages VALIDATE CONSTRAINT inbox_compat_distinct_participants;

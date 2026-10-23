-- SPDX-License-Identifier: AGPL-3.0-only
-- aeon:contract-phase AEON-1521 expanded-in=v261022082852.0.0 expansion-migration=0898_session_messages.sql
-- The released session expansion already binds both exact generations. The
-- raw inbox has tenant-scoped sender/recipient FKs; the compatibility sender
-- FK additionally, and unnecessarily, confined the sender to the recipient's
-- project. Retain tenant isolation while allowing a cross-project sender.
-- Older binaries only write different-principal, same-project messages and
-- remain valid under these broader constraints. No rows or columns change.
ALTER TABLE inbox_compat_messages
 DROP CONSTRAINT inbox_compat_sender_session_fk,
 ADD CONSTRAINT inbox_compat_sender_session_fk
 FOREIGN KEY (tenant_id,sender_session_id) REFERENCES harness_sessions(tenant_id,id) NOT VALID;

-- A workstation principal can own multiple sessions. Self-addressing is
-- allowed only between two distinct, explicitly bound generations; the peer
-- handler checks both current owners under the access-change fence. Unbound
-- principal self-messages and sends to the same generation remain forbidden.
ALTER TABLE inbox_messages
 DROP CONSTRAINT inbox_messages_check2,
 ADD CONSTRAINT inbox_messages_distinct_participants CHECK (
  sender_principal_id<>recipient_principal_id OR
  (sender_session_id IS NOT NULL AND recipient_session_id IS NOT NULL AND sender_session_id<>recipient_session_id)
 ) NOT VALID;
ALTER TABLE inbox_compat_messages
 DROP CONSTRAINT inbox_compat_messages_check,
 ADD CONSTRAINT inbox_compat_distinct_participants CHECK (
  sender_principal_id<>recipient_principal_id OR
  (sender_session_id IS NOT NULL AND recipient_session_id IS NOT NULL AND sender_session_id<>recipient_session_id)
 ) NOT VALID;

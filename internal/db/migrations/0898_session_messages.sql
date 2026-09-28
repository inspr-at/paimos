-- SPDX-License-Identifier: AGPL-3.0-only
-- Metadata-only nullable additions. Release ACCESS EXCLUSIVE before any scans.
-- Old rows remain unbound: a principal's current generation cannot own history.
ALTER TABLE inbox_messages
 ADD COLUMN recipient_session_id uuid,
 ADD COLUMN sender_session_id uuid,
 ADD COLUMN sender_label text;
ALTER TABLE inbox_compat_messages
 ADD COLUMN recipient_session_id uuid,
 ADD COLUMN sender_session_id uuid,
 ADD COLUMN sender_label text;
ALTER TABLE inbox_reply_obligations ADD COLUMN closed_reason text;

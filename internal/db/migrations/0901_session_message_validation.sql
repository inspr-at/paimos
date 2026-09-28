-- SPDX-License-Identifier: AGPL-3.0-only
-- SHARE UPDATE EXCLUSIVE validation permits normal reads and writes.
ALTER TABLE inbox_messages VALIDATE CONSTRAINT inbox_messages_recipient_session_fk;
ALTER TABLE inbox_messages VALIDATE CONSTRAINT inbox_messages_sender_session_fk;
ALTER TABLE inbox_compat_messages VALIDATE CONSTRAINT inbox_compat_recipient_session_fk;
ALTER TABLE inbox_compat_messages VALIDATE CONSTRAINT inbox_compat_sender_session_fk;
ALTER TABLE inbox_reply_obligations VALIDATE CONSTRAINT inbox_reply_obligations_closed_reason_check;
ALTER TABLE inbox_reply_obligations VALIDATE CONSTRAINT inbox_reply_obligations_closure;

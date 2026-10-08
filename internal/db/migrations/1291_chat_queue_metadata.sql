-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-977: expand-only metadata; existing writers may omit both columns.
ALTER TABLE inbox_messages ADD COLUMN cancelled_at timestamptz;
ALTER TABLE inbox_compat_messages ADD COLUMN resend_of_id uuid;

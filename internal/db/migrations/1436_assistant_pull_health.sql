-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand-only: existing assistants need no owner action or rewritten binding.
-- Delivery mode is already immutable in inbox_message_targets.target_kind:
-- https_webhook is push; pull uses only the durable principal inbox.
ALTER TABLE principals ADD COLUMN assistant_last_pull_at timestamptz;

CREATE INDEX inbox_assistant_oldest_unread ON inbox_messages
    (tenant_id, recipient_principal_id, created_at)
    WHERE content_mode='durable' AND chat_thread_id IS NULL
      AND recipient_session_id IS NULL AND acked_at IS NULL;

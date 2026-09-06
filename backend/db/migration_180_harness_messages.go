// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
)

// applyHarnessMessagesMigration180 adds generation-bound human conversation
// identity and immutable idempotency receipts. It stays separate from db.go so
// the preceding delivery-recovery migration can land without shared-file edits.
func applyHarnessMessagesMigration180(ctx context.Context, conn *sql.Conn) error {
	return applyMigrationAtomic(ctx, conn, migration{version: 180, steps: []string{
		`CREATE TABLE harness_conversation_bindings (
		 instance                  TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
		 project_id                INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		 user_id                   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		 harness_session_id        TEXT NOT NULL REFERENCES harness_sessions(id) ON DELETE RESTRICT,
		 product_session_id        TEXT NOT NULL UNIQUE REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
		 created_at                TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 PRIMARY KEY(instance,project_id,user_id,harness_session_id)
		) WITHOUT ROWID`,
		`CREATE TRIGGER trg_harness_conversation_bindings_shape BEFORE INSERT ON harness_conversation_bindings
		 WHEN NOT EXISTS(
		  SELECT 1 FROM harness_sessions harness
		  JOIN product_sessions session ON session.product_session_id=NEW.product_session_id
		  WHERE harness.id=NEW.harness_session_id AND harness.project_id=NEW.project_id
		   AND session.project_id=NEW.project_id AND session.target_kind='project_agent'
		   AND session.target_project_agent_id=harness.project_agent_id
		   AND session.created_by_user_id=NEW.user_id)
		 BEGIN SELECT RAISE(ABORT,'harness conversation binding mismatch'); END`,
		`CREATE TRIGGER trg_harness_conversation_bindings_no_update BEFORE UPDATE ON harness_conversation_bindings
		 BEGIN SELECT RAISE(ABORT,'harness conversation bindings are immutable'); END`,
		`CREATE TRIGGER trg_harness_conversation_bindings_no_delete BEFORE DELETE ON harness_conversation_bindings
		 WHEN EXISTS(SELECT 1 FROM harness_sessions WHERE id=OLD.harness_session_id)
		 BEGIN SELECT RAISE(ABORT,'harness conversation bindings are immutable'); END`,
		`CREATE TABLE harness_message_receipts (
		 instance                  TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
		 project_id                INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		 user_id                   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		 utterance_id              TEXT NOT NULL CHECK(length(CAST(utterance_id AS BLOB))=36 AND
		  substr(utterance_id,1,4)='utt_' AND substr(utterance_id,5) NOT GLOB '*[^0-9a-f]*'),
		 request_digest            BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
		 harness_session_id        TEXT NOT NULL REFERENCES harness_sessions(id) ON DELETE RESTRICT,
		 harness_session_revision  INTEGER NOT NULL CHECK(harness_session_revision>0),
		 runtime_id                TEXT NOT NULL REFERENCES lifecycle_runtimes(id) ON DELETE RESTRICT,
		 runtime_generation        TEXT NOT NULL CHECK(length(CAST(runtime_generation AS BLOB)) BETWEEN 1 AND 64),
		 session_generation        TEXT NOT NULL CHECK(length(CAST(session_generation AS BLOB)) BETWEEN 1 AND 64),
		 target_id                 TEXT NOT NULL REFERENCES agent_message_targets(id) ON DELETE RESTRICT,
		 message_row_id            INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE RESTRICT,
		 product_session_id        TEXT NOT NULL REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
		 delivery_id               TEXT NOT NULL UNIQUE REFERENCES agent_message_deliveries(delivery_id) ON DELETE RESTRICT,
		 delivery_level            TEXT NOT NULL CHECK(delivery_level IN ('simple','steer')),
		 created_at                TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 PRIMARY KEY(instance,project_id,user_id,utterance_id)
		) WITHOUT ROWID`,
		`CREATE TRIGGER trg_harness_message_receipts_shape BEFORE INSERT ON harness_message_receipts
		 WHEN NOT EXISTS(
		  SELECT 1 FROM harness_conversation_bindings binding
		  JOIN harness_sessions harness ON harness.id=binding.harness_session_id
		  JOIN lifecycle_runtime_sessions owned ON owned.session_id=harness.id
		  JOIN lifecycle_runtimes runtime ON runtime.id=owned.runtime_id
		  JOIN agent_messages message ON message.id=NEW.message_row_id
		  JOIN agent_message_deliveries delivery ON delivery.delivery_id=NEW.delivery_id
		  WHERE binding.instance=NEW.instance AND binding.project_id=NEW.project_id
		   AND binding.user_id=NEW.user_id AND binding.harness_session_id=NEW.harness_session_id
		   AND binding.product_session_id=NEW.product_session_id
		   AND harness.revision=NEW.harness_session_revision
		   AND owned.runtime_id=NEW.runtime_id AND owned.generation=NEW.session_generation
		   AND runtime.generation=NEW.runtime_generation
		   AND message.role='human' AND message.from_agent_id IS NULL
		   AND message.from_user_id=NEW.user_id AND message.to_agent_id=harness.project_agent_id
		   AND message.issue_id IS harness.ticket_id
		   AND message.product_session_id=NEW.product_session_id
		   AND message.to_address=lower(harness.harness)||':'||harness.agent_name
		   AND message.delivery_primary_target_id=NEW.target_id
		   AND message.delivery_fallback_target_id IS NULL
		   AND message.delivery_level=NEW.delivery_level
		   AND delivery.message_row_id=message.id AND delivery.instance=NEW.instance
		   AND delivery.primary_target_id=NEW.target_id AND delivery.fallback_target_id IS NULL
		   AND delivery.requested_level=NEW.delivery_level)
		 BEGIN SELECT RAISE(ABORT,'harness message receipt mismatch'); END`,
		`CREATE TRIGGER trg_harness_message_receipts_no_update BEFORE UPDATE ON harness_message_receipts
		 BEGIN SELECT RAISE(ABORT,'harness message receipts are immutable'); END`,
		`CREATE TRIGGER trg_harness_message_receipts_no_delete BEFORE DELETE ON harness_message_receipts
		 WHEN EXISTS(SELECT 1 FROM agent_messages WHERE id=OLD.message_row_id)
		 BEGIN SELECT RAISE(ABORT,'harness message receipts are immutable'); END`,
	}})
}

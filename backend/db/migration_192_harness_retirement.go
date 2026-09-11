// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
)

// applyHarnessRetirementMigration192 adds an append-preserving retirement
// ledger. A retirement row fences new assignment and delivery admission while
// leaving the session, workspace, credentials, messages, and history intact.
func applyHarnessRetirementMigration192(ctx context.Context, conn *sql.Conn) error {
	return applyMigrationAtomic(ctx, conn, migration{version: 192, steps: []string{
		`CREATE TABLE harness_session_retirements (
		 id                            TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("id") + `),
		 project_id                    INTEGER NOT NULL REFERENCES projects(id),
		 harness_session_id            TEXT NOT NULL REFERENCES harness_sessions(id),
		 harness_session_revision      INTEGER NOT NULL CHECK(harness_session_revision>0),
		 requested_activity_sequence   INTEGER NOT NULL CHECK(requested_activity_sequence>=0),
		 runtime_id                    TEXT NOT NULL REFERENCES lifecycle_runtimes(id),
		 runtime_generation            TEXT NOT NULL,
		 session_generation            TEXT NOT NULL,
		 ticket_id                     INTEGER REFERENCES issues(id),
		 parent_harness_session_id     TEXT REFERENCES harness_sessions(id),
		 work_shape                    TEXT NOT NULL CHECK(work_shape IN ('unknown','ship','scout')),
		 request_key                   TEXT NOT NULL CHECK(` + sqlUUIDCheck("request_key") + `),
		 request_digest                BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
		 requested_by_user_id          INTEGER NOT NULL REFERENCES users(id),
		 requested_session_credential_id TEXT NOT NULL,
		 state                         TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','claimed','stopping','applied','rejected')),
		 reason                        TEXT NOT NULL DEFAULT '' CHECK(reason IN ('','applied','not_running','unsupported','ownership_lost','failed','outcome_unknown')),
		 requested_at                  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("requested_at") + `),
		 claimed_at                    TEXT CHECK(` + sqlNullableControlTimestampCheck("claimed_at") + `),
		 stopping_at                   TEXT CHECK(` + sqlNullableControlTimestampCheck("stopping_at") + `),
		 completed_at                  TEXT CHECK(` + sqlNullableControlTimestampCheck("completed_at") + `),
		 UNIQUE(project_id,requested_by_user_id,request_key),
		 CHECK((ticket_id IS NULL AND work_shape='unknown') OR ticket_id IS NOT NULL),
		 CHECK((state='pending' AND claimed_at IS NULL AND stopping_at IS NULL AND completed_at IS NULL AND reason='') OR
		       (state='claimed' AND claimed_at IS NOT NULL AND stopping_at IS NULL AND completed_at IS NULL AND reason='') OR
		       (state='stopping' AND claimed_at IS NOT NULL AND stopping_at IS NOT NULL AND completed_at IS NULL AND reason='') OR
		       (state IN ('applied','rejected') AND claimed_at IS NOT NULL AND completed_at IS NOT NULL AND reason<>''))
		)`,
		`CREATE INDEX idx_harness_session_retirements_runtime
		 ON harness_session_retirements(runtime_id,runtime_generation,session_generation,state)`,
		`CREATE UNIQUE INDEX idx_harness_session_retirements_active
		 ON harness_session_retirements(harness_session_id)
		 WHERE state<>'rejected' OR reason='outcome_unknown'`,
		`CREATE TRIGGER trg_harness_session_retirement_identity BEFORE UPDATE OF
		 id,project_id,harness_session_id,harness_session_revision,requested_activity_sequence,
		 runtime_id,runtime_generation,session_generation,ticket_id,parent_harness_session_id,
		 work_shape,request_key,request_digest,requested_by_user_id,requested_session_credential_id,requested_at
		 ON harness_session_retirements BEGIN SELECT RAISE(ABORT,'harness retirement identity is immutable'); END`,
		`CREATE TRIGGER trg_harness_session_retirement_transition BEFORE UPDATE OF state,reason,claimed_at,stopping_at,completed_at
		 ON harness_session_retirements WHEN NOT (
		  (OLD.state='pending' AND NEW.state IN ('claimed','rejected')) OR
		  (OLD.state='claimed' AND NEW.state IN ('stopping','rejected')) OR
		  (OLD.state='stopping' AND NEW.state IN ('applied','rejected'))
		 ) BEGIN SELECT RAISE(ABORT,'invalid harness retirement transition'); END`,
		`CREATE TRIGGER trg_harness_session_retirement_no_delete BEFORE DELETE ON harness_session_retirements
		 BEGIN SELECT RAISE(ABORT,'harness retirement history is immutable'); END`,
		`CREATE TRIGGER trg_harness_retirement_binding_admission BEFORE UPDATE OF
		 parent_harness_session_id,ticket_id,work_shape ON harness_sessions
		 WHEN EXISTS(SELECT 1 FROM harness_session_retirements retirement
		  WHERE retirement.harness_session_id=OLD.id AND
		   (retirement.state<>'rejected' OR retirement.reason='outcome_unknown'))
		 BEGIN SELECT RAISE(ABORT,'harness retirement blocks assignment admission'); END`,
		`CREATE TRIGGER trg_harness_retirement_delivery_admission BEFORE UPDATE OF state ON agent_message_deliveries
		 WHEN NEW.state='leased' AND OLD.state<>'leased' AND EXISTS(
		  SELECT 1 FROM harness_session_retirements retirement
		  JOIN harness_sessions session ON session.id=retirement.harness_session_id
		  WHERE (session.message_target_id=NEW.primary_target_id OR session.message_target_id=NEW.fallback_target_id)
		   AND (retirement.state<>'rejected' OR retirement.reason='outcome_unknown'))
		 BEGIN SELECT RAISE(ABORT,'harness retirement blocks delivery admission'); END`,
		`CREATE TRIGGER trg_harness_retirement_consumer_admission BEFORE INSERT ON agent_consumer_attempts
		 WHEN EXISTS(SELECT 1 FROM agent_consumer_streams stream
		  JOIN harness_session_retirements retirement ON retirement.harness_session_id=stream.session_id
		  WHERE stream.id=NEW.stream_id AND (retirement.state<>'rejected' OR retirement.reason='outcome_unknown'))
		 BEGIN SELECT RAISE(ABORT,'harness retirement blocks consumer admission'); END`,
		`CREATE TRIGGER trg_harness_retirement_control_admission BEFORE INSERT ON harness_session_controls
		 WHEN EXISTS(SELECT 1 FROM harness_session_retirements retirement
		  WHERE retirement.harness_session_id=NEW.harness_session_id AND
		   (retirement.state<>'rejected' OR retirement.reason='outcome_unknown'))
		 BEGIN SELECT RAISE(ABORT,'harness retirement blocks competing controls'); END`,
	}})
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/lifecyclefence"
	"github.com/inspr-at/paimos/backend/safetext"
)

const (
	browserMessageSchemaVersion = 1
	browserMessageMaxTextBytes  = 8 * 1024
)

var (
	browserMessageIDPattern = regexp.MustCompile(`^utt_[0-9a-f]{32}$`)
	browserMessageMu        sync.Mutex
)

const (
	browserTargetLevelSimple = "target.maximum_level IN ('simple','steer')"
	browserTargetLevelSteer  = "target.maximum_level='steer'"
)

var browserTargetQueryPrefix = `SELECT owned.runtime_id,runtime.generation,owned.generation,target.id,
		runtime.user_id,runtime.api_key_id
		FROM harness_sessions harness
		JOIN lifecycle_runtime_sessions owned ON owned.session_id=harness.id
		JOIN lifecycle_runtimes runtime ON runtime.id=owned.runtime_id
		 AND runtime.project_id=harness.project_id AND runtime.machine_id=harness.host
		 AND ` + lifecyclefence.OwnershipSQLRuntimeHarness + `
		JOIN agent_message_targets target ON target.id=harness.message_target_id
		 AND target.instance=? AND target.project_id=harness.project_id AND target.enabled=1
		 AND target.role='primary' AND target.adapter='managed_harness'
		 AND target.target_kind='harness_session'
		 AND target.address=lower(harness.harness)||':'||harness.agent_name
		WHERE harness.project_id=? AND harness.id=? AND harness.management_mode='managed'
		 AND harness.phase NOT IN ('stopping','stopped') AND harness.revision=? AND runtime.expires_at>?
		 AND `

var browserTargetQuerySuffix = `
		 AND (CASE WHEN harness.phase='starting' THEN harness.updated_at>=?
		           ELSE harness.heartbeat_at IS NOT NULL AND harness.heartbeat_at>=? END)`

var (
	browserTargetQuerySimple = browserTargetQueryPrefix + browserTargetLevelSimple + browserTargetQuerySuffix
	browserTargetQuerySteer  = browserTargetQueryPrefix + browserTargetLevelSteer + browserTargetQuerySuffix
)

type BrowserMessageRequest struct {
	SchemaVersion    int    `json:"schema_version"`
	UtteranceID      string `json:"utterance_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Text             string `json:"text"`
	DeliveryLevel    string `json:"delivery_level"`
}

type BrowserMessageResponse struct {
	SchemaVersion          int    `json:"schema_version"`
	UtteranceID            string `json:"utterance_id"`
	HarnessSessionID       string `json:"harness_session_id"`
	HarnessSessionRevision int64  `json:"harness_session_revision"`
	MessageID              string `json:"message_id"`
	DeliveryID             string `json:"delivery_id"`
	DeliveryLevel          string `json:"delivery_level"`
	CreatedAt              string `json:"created_at"`
}

type browserMessageTarget struct {
	runtimeID         string
	runtimeGeneration string
	sessionGeneration string
	targetID          string
	runtimeUserID     int64
	runtimeAPIKeyID   int64
}

func validBrowserMessageText(value string) bool {
	if !utf8.ValidString(value) || value == "" || value != strings.TrimSpace(value) || len([]byte(value)) > browserMessageMaxTextBytes || safetext.MessageBodyContainsSecretLike(value) {
		return false
	}
	for _, r := range value {
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f {
			return false
		}
	}
	return true
}

func validBrowserMessageRequest(in BrowserMessageRequest) bool {
	return in.SchemaVersion == browserMessageSchemaVersion && browserMessageIDPattern.MatchString(in.UtteranceID) &&
		in.ExpectedRevision > 0 && validBrowserMessageText(in.Text) &&
		(in.DeliveryLevel == "simple" || in.DeliveryLevel == "steer")
}

func browserMessageDigest(project int64, sessionID string, in BrowserMessageRequest) ([]byte, error) {
	canonical := struct {
		Domain           string `json:"domain"`
		ProjectID        int64  `json:"project_id"`
		HarnessSessionID string `json:"harness_session_id"`
		Revision         int64  `json:"expected_revision"`
		Text             string `json:"text"`
		DeliveryLevel    string `json:"delivery_level"`
	}{"paimos.harness-browser-message.v1", project, sessionID, in.ExpectedRevision, in.Text, in.DeliveryLevel}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func browserMessageStamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func newBrowserMessageUUID() uuid.UUID {
	value, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return value
}

func loadBrowserMessageReceipt(ctx context.Context, tx *sql.Tx, instance string, project, user int64, utteranceID string) (BrowserMessageResponse, []byte, bool, error) {
	var out BrowserMessageResponse
	var digest []byte
	err := tx.QueryRowContext(ctx, `SELECT receipt.request_digest,receipt.harness_session_id,
		receipt.harness_session_revision,message.message_id,receipt.delivery_id,
		receipt.delivery_level,receipt.created_at
		FROM harness_message_receipts receipt
		JOIN agent_messages message ON message.id=receipt.message_row_id
		WHERE receipt.instance=? AND receipt.project_id=? AND receipt.user_id=? AND receipt.utterance_id=?`,
		instance, project, user, utteranceID).Scan(&digest, &out.HarnessSessionID,
		&out.HarnessSessionRevision, &out.MessageID, &out.DeliveryID, &out.DeliveryLevel, &out.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil, false, nil
	}
	if err != nil {
		return out, nil, false, err
	}
	out.SchemaVersion, out.UtteranceID = browserMessageSchemaVersion, utteranceID
	return out, digest, true, nil
}

func ensureBrowserConversation(ctx context.Context, tx *sql.Tx, instance string, project, user int64, session HarnessSessionSnapshot, createdAt string) (string, error) {
	var productSessionID string
	err := tx.QueryRowContext(ctx, `SELECT product_session_id FROM harness_conversation_bindings
		WHERE instance=? AND project_id=? AND user_id=? AND harness_session_id=?`,
		instance, project, user, session.ID).Scan(&productSessionID)
	if err == nil {
		return productSessionID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	productSessionID = uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,node_id,title,summary,revision,
		created_by_user_id,updated_by_user_id,created_at,updated_at)
		VALUES(?,?,'project_agent',?,?,?,'',1,?,?,?,?)`, productSessionID, project,
		session.ProjectAgentID, session.TicketID, "Habitat · "+session.AgentName, user, user, createdAt, createdAt)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO harness_conversation_bindings(
		instance,project_id,user_id,harness_session_id,product_session_id,created_at) VALUES(?,?,?,?,?,?)`,
		instance, project, user, session.ID, productSessionID, createdAt)
	return productSessionID, err
}

// SendBrowserMessageCAS writes one canonical human message to the exact
// runtime-owned generation selected in Habitat. Replays are resolved only
// after current human authority has been revalidated.
func (s *Service) SendBrowserMessageCAS(ctx context.Context, p auth.Principal, project int64, sessionID string, in BrowserMessageRequest) (BrowserMessageResponse, error) {
	if s == nil || s.db == nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	browserMessageMu.Lock()
	defer browserMessageMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	defer tx.Rollback()
	if err = browserAuthority(ctx, tx, p, project, true); err != nil {
		return BrowserMessageResponse{}, err
	}
	if !validBrowserMessageRequest(in) || uuid.Validate(sessionID) != nil {
		return BrowserMessageResponse{}, ErrBrowserInvalid
	}
	instance := strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_INSTANCE"))
	if len([]byte(instance)) < 1 || len([]byte(instance)) > 64 {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	digest, err := browserMessageDigest(project, sessionID, in)
	if err != nil {
		return BrowserMessageResponse{}, ErrBrowserInvalid
	}
	if prior, priorDigest, found, loadErr := loadBrowserMessageReceipt(ctx, tx, instance, project, p.UserID(), in.UtteranceID); loadErr != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	} else if found {
		if !bytes.Equal(priorDigest, digest) {
			return BrowserMessageResponse{}, ErrBrowserConflict
		}
		if tx.Commit() != nil {
			return BrowserMessageResponse{}, ErrBrowserStorage
		}
		return prior, nil
	}

	current, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=? AND project_id=?`, sessionID, project))
	if err != nil || current.ManagementMode != ManagementManaged ||
		current.Phase == PhaseStopping || current.Phase == PhaseStopped ||
		(in.DeliveryLevel == "steer" && !current.Capabilities.Steer) ||
		(in.DeliveryLevel == "simple" && !current.Capabilities.Inbox) {
		return BrowserMessageResponse{}, ErrBrowserUnavailable
	}
	if current.Revision != in.ExpectedRevision {
		return BrowserMessageResponse{}, ErrBrowserConflict
	}
	now := time.Now().UTC()
	createdAt := browserMessageStamp(now)
	stale := browserMessageStamp(now.Add(-90 * time.Second))
	var target browserMessageTarget
	targetQuery := browserTargetQuerySimple
	if in.DeliveryLevel == "steer" {
		targetQuery = browserTargetQuerySteer
	}
	err = tx.QueryRowContext(ctx, targetQuery, instance, project, sessionID, in.ExpectedRevision,
		createdAt, stale, stale).Scan(&target.runtimeID, &target.runtimeGeneration, &target.sessionGeneration,
		&target.targetID, &target.runtimeUserID, &target.runtimeAPIKeyID)
	if err != nil {
		return BrowserMessageResponse{}, ErrBrowserUnavailable
	}
	if _, _, err = auth.ReauthorizeRuntimeReporterTx(ctx, tx, target.runtimeUserID, target.runtimeAPIKeyID, project, now); err != nil {
		return BrowserMessageResponse{}, ErrBrowserUnavailable
	}
	productSessionID, err := ensureBrowserConversation(ctx, tx, instance, project, p.UserID(), HarnessSessionSnapshot{
		ID: current.ID, ProjectAgentID: current.ProjectAgentID, AgentName: current.AgentName,
		TicketID: current.TicketID,
	}, createdAt)
	if err != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	projectKey := ""
	if tx.QueryRowContext(ctx, `SELECT key FROM projects WHERE id=? AND status<>'deleted'`, project).Scan(&projectKey) != nil {
		return BrowserMessageResponse{}, ErrBrowserUnavailable
	}
	taskID := ""
	if current.TicketID != nil {
		if tx.QueryRowContext(ctx, `SELECT project.key||'-'||issue.issue_number FROM issues issue
			JOIN projects project ON project.id=issue.project_id
			WHERE issue.id=? AND issue.project_id=? AND issue.deleted_at IS NULL`, *current.TicketID, project).Scan(&taskID) != nil {
			return BrowserMessageResponse{}, ErrBrowserUnavailable
		}
	}
	parts, _ := json.Marshal([]map[string]string{{"kind": "text", "text": in.Text}})
	messageID, deliveryID := newBrowserMessageUUID().String(), newBrowserMessageUUID().String()
	toAddress := strings.ToLower(current.Harness) + ":" + current.AgentName
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_messages(
		from_agent_id,to_agent_id,issue_id,hop_count,body,is_action_request,delivered,held_reason,created_at,delivered_at,
		message_id,context_id,task_id,role,parts_json,metadata_json,from_address,to_address,reply_to,thread_id,
		session_id,read_at,delivery_level,delivery_fallback,delivery_primary_target_id,delivery_fallback_target_id,
		from_user_id,product_session_id)
		VALUES(NULL,?,?,1,?,0,1,'',?,?,?, ?,?,'human',?,'{}',?,?,'',?,'',NULL,?,'simple',?,NULL,?,?)`,
		current.ProjectAgentID, current.TicketID, in.Text, createdAt, createdAt, messageID, projectKey, taskID,
		string(parts), fmt.Sprintf("user:%d", p.UserID()), toAddress, productSessionID,
		in.DeliveryLevel, target.targetID, p.UserID(), productSessionID)
	if err != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	messageRowID, err := result.LastInsertId()
	if err != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_message_deliveries(
		delivery_id,message_row_id,instance,primary_target_id,fallback_target_id,requested_level,state)
		VALUES(?,?,?,?,NULL,?,'pending')`, deliveryID, messageRowID, instance, target.targetID, in.DeliveryLevel); err != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO harness_message_receipts(
		instance,project_id,user_id,utterance_id,request_digest,harness_session_id,harness_session_revision,
		runtime_id,runtime_generation,session_generation,target_id,message_row_id,product_session_id,
		delivery_id,delivery_level,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		instance, project, p.UserID(), in.UtteranceID, digest, current.ID, current.Revision,
		target.runtimeID, target.runtimeGeneration, target.sessionGeneration, target.targetID, messageRowID,
		productSessionID, deliveryID, in.DeliveryLevel, createdAt); err != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	if tx.Commit() != nil {
		return BrowserMessageResponse{}, ErrBrowserStorage
	}
	return BrowserMessageResponse{SchemaVersion: browserMessageSchemaVersion, UtteranceID: in.UtteranceID,
		HarnessSessionID: current.ID, HarnessSessionRevision: current.Revision, MessageID: messageID,
		DeliveryID: deliveryID, DeliveryLevel: in.DeliveryLevel, CreatedAt: createdAt}, nil
}

// HarnessSessionSnapshot is the immutable subset needed to create a durable
// human conversation while the selected harness row is locked by the caller's
// transaction.
type HarnessSessionSnapshot struct {
	ID             string
	ProjectAgentID int64
	AgentName      string
	TicketID       *int64
}

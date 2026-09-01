// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
	"github.com/inspr-at/paimos/backend/secretvault"
)

// Receiver-owned target references and their optional sender secrets live in
// separate secretvault domains so one ciphertext can never be replayed as the
// other.
const (
	targetSecretDomain       = "agent-message-targets"
	targetSenderSecretDomain = "agent-message-target-secrets"
)

// Registry adapters and target kinds. Local adapters are executed by an
// attributed receiver-side worker that leases the delivery through listen and
// records the handoff through delivery-complete; grok_bot_routine is
// dispatched server-side by the webhook dispatcher instead.
const (
	AdapterCodex          = harnessplugin.AdapterCodex
	AdapterAgentdCodex    = harnessplugin.AdapterAgentdCodex
	AdapterAgentdClaude   = harnessplugin.AdapterAgentdClaude
	AdapterGrokBotRoutine = harnessplugin.AdapterGrokBotRoutine
	AdapterClaudeResume   = harnessplugin.AdapterClaudeResume
	AdapterClaudeChannel  = harnessplugin.AdapterClaudeChannel
	AdapterManagedHarness = harnessplugin.AdapterManagedHarness

	TargetKindCodexThread    = harnessplugin.KindCodexThread
	TargetKindAgentdSession  = harnessplugin.KindAgentdSession
	TargetKindHTTPSWebhook   = harnessplugin.KindHTTPSWebhook
	TargetKindClaudeSession  = harnessplugin.KindClaudeSession
	TargetKindHarnessSession = harnessplugin.KindHarnessSession
)

// ClaudeSessionPrimitive maps a Claude session reference to the documented
// print-mode flag that addresses it: --resume for a local session UUID and
// --cloud for a cloud session id. ok is false for every other shape.
func ClaudeSessionPrimitive(ref string) (flag string, ok bool) {
	return harnessplugin.ClaudeSessionPrimitive(ref)
}

// IsLocalWorkerAdapter reports whether adapter names a delivery adapter that
// an attributed receiver-side worker executes and completes locally.
func IsLocalWorkerAdapter(adapter string) bool {
	plugin, err := harnessplugin.Lookup(adapter)
	return err == nil && plugin.Mode() == harnessplugin.ModeLocal
}

const selectedDeliveryTargetSQL = `(CASE
	WHEN d.last_error_code='managed_target_unavailable' AND d.fallback_target_id IS NOT NULL
	THEN d.fallback_target_id
	WHEN d.requested_level='simple' AND d.primary_target_id IS NOT NULL AND d.fallback_target_id IS NOT NULL
	 AND (SELECT adapter FROM agent_message_targets policy_target WHERE policy_target.id=d.primary_target_id) IN ('agentd_codex','agentd_claude')
	THEN d.fallback_target_id
	WHEN d.requested_level='steer' AND d.primary_target_id IS NOT NULL AND d.fallback_target_id IS NOT NULL
	 AND (SELECT maximum_level FROM agent_message_targets policy_target WHERE policy_target.id=d.primary_target_id)='simple'
	THEN d.fallback_target_id ELSE COALESCE(d.primary_target_id,d.fallback_target_id) END)`

// ManagedGenerationLivenessWindow aligns reroute eligibility with the M161
// heartbeat contract: three missed 30-second heartbeats make a working row
// stale. Phase alone is never live-process proof.
const ManagedGenerationLivenessWindow = 90 * time.Second

// Target is the non-secret operator view of an immutable receiver target
// version. TargetRef and the sender secret are deliberately absent; HasSecret
// only reports that an encrypted sender secret is bound to this version.
type Target struct {
	ID           string `json:"id"`
	Instance     string `json:"instance"`
	ProjectID    int64  `json:"project_id"`
	Address      string `json:"address"`
	Adapter      string `json:"adapter"`
	TargetKind   string `json:"target_kind"`
	MaximumLevel string `json:"maximum_level"`
	Role         string `json:"role"`
	Enabled      bool   `json:"enabled"`
	Version      int    `json:"version"`
	HasSecret    bool   `json:"has_secret"`
	CreatedAt    string `json:"created_at"`
}

type RegisterTargetInput struct {
	ProjectID    int64
	Address      string
	Adapter      string
	TargetKind   string
	TargetRef    string
	TargetSecret string
	MaximumLevel string
	Role         string
	// Standby creates a disabled managed_harness primary without replacing an
	// enabled agentd managed primary. It is reserved for M161 generation reroute.
	Standby bool
}

const targetSelectColumns = `id,instance,project_id,address,adapter,target_kind,maximum_level,role,enabled,version,
	target_secret_cipher IS NOT NULL,created_at`

func scanTarget(row interface{ Scan(...any) error }, target *Target) error {
	return row.Scan(&target.ID, &target.Instance, &target.ProjectID, &target.Address, &target.Adapter, &target.TargetKind,
		&target.MaximumLevel, &target.Role, &target.Enabled, &target.Version, &target.HasSecret, &target.CreatedAt)
}

func instanceName() string {
	name := strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_INSTANCE"))
	if name == "" {
		return "default"
	}
	if len([]byte(name)) > 64 || !utf8.ValidString(name) {
		return ""
	}
	return name
}

// InstanceName returns the non-secret identity of this process's durable
// Agent Intercom ledger. It is safe to expose in operational health evidence.
func InstanceName() string {
	return instanceName()
}

// ValidateInstanceIdentity is the startup/doctor firewall for the durable bus.
// Development may keep the historical default when no deployment identity is
// configured, but production must name its domain and any configured instance
// must match it exactly.
func ValidateInstanceIdentity(expected string, production bool) error {
	raw := strings.TrimSpace(os.Getenv("PAIMOS_AGENT_BUS_INSTANCE"))
	name := instanceName()
	if name == "" {
		return fmt.Errorf("PAIMOS_AGENT_BUS_INSTANCE must be 1 to 64 valid UTF-8 bytes")
	}
	if production && (raw == "" || name == "default") {
		return fmt.Errorf("production requires PAIMOS_AGENT_BUS_INSTANCE to be an explicit non-default instance ID")
	}
	expected = strings.TrimSpace(expected)
	if production && (expected == "" || expected == "default") {
		return fmt.Errorf("production requires PAIMOS_DEPLOYMENT_INSTANCE to be an explicit non-default deployment ID")
	}
	if expected != "" && raw != "" && name != expected {
		return fmt.Errorf("PAIMOS_AGENT_BUS_INSTANCE %q does not match configured instance %q", name, expected)
	}
	return nil
}

// RegisterTarget creates a new encrypted target version and atomically makes
// it the one enabled binding for its receiver role.
func (s *Service) RegisterTarget(ctx context.Context, in RegisterTargetInput) (*Target, error) {
	harness, agent, err := parseAddress(in.Address)
	if err != nil {
		return nil, err
	}
	in.Address = harness + ":" + agent
	in.Adapter = strings.ToLower(strings.TrimSpace(in.Adapter))
	in.TargetKind = strings.ToLower(strings.TrimSpace(in.TargetKind))
	in.MaximumLevel = strings.ToLower(strings.TrimSpace(in.MaximumLevel))
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	if in.MaximumLevel == "" {
		in.MaximumLevel = "simple"
	}
	if in.Role == "" {
		in.Role = "primary"
	}
	if in.MaximumLevel != "simple" && in.MaximumLevel != "steer" {
		return nil, coded("agent_message_target_level_invalid", "maximum_level must be simple or steer")
	}
	if in.Role != "primary" && in.Role != "simple_fallback" {
		return nil, coded("agent_message_target_role_invalid", "role must be primary or simple_fallback")
	}
	if in.Role == "simple_fallback" && in.MaximumLevel != "simple" {
		return nil, coded("agent_message_target_level_invalid", "a simple_fallback target must have maximum_level simple")
	}
	if in.Standby && (in.Adapter != AdapterManagedHarness || in.Role != "primary") {
		return nil, coded("agent_message_target_role_invalid", "standby targets are reserved for managed_harness primary generations")
	}
	ref := strings.TrimSpace(in.TargetRef)
	if !utf8.ValidString(ref) || len([]byte(ref)) < 1 || len([]byte(ref)) > 2048 || strings.ContainsAny(ref, "\x00\r\n") {
		return nil, coded("agent_message_target_ref_invalid", "target_ref must be 1 to 2048 safe UTF-8 bytes")
	}
	if err := harnessplugin.ValidateBinding(ctx, in.Adapter, in.TargetKind, in.MaximumLevel, ref); err != nil {
		code := harnessplugin.ErrorCode(err)
		if code == harnessplugin.CodeUnsupported {
			return nil, coded("agent_message_target_adapter_invalid", err.Error())
		}
		if code == "" {
			code = "agent_message_target_ref_invalid"
		}
		return nil, coded(code, err.Error())
	}
	secret := strings.TrimSpace(in.TargetSecret)
	if err := harnessplugin.ValidateSecret(in.Adapter, secret); err != nil {
		code := harnessplugin.ErrorCode(err)
		if code == "" || code == harnessplugin.CodeUnsupported {
			code = harnessplugin.CodeTargetSecretInvalid
		}
		return nil, coded(code, err.Error())
	}
	ciphertext, err := secretvault.Encrypt(targetSecretDomain, []byte(ref))
	if err != nil {
		return nil, fmt.Errorf("encrypt agent message target: %w", err)
	}
	var secretCipher []byte
	if secret != "" {
		secretCipher, err = secretvault.Encrypt(targetSenderSecretDomain, []byte(secret))
		if err != nil {
			return nil, fmt.Errorf("encrypt agent message target secret: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_agents WHERE project_id=? AND name=?`, in.ProjectID, agent).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, coded("agent_message_addressee_unknown", "target receiver is not registered in this project")
	}
	instance := instanceName()
	if instance == "" {
		return nil, coded("agent_message_instance_invalid", "PAIMOS_AGENT_BUS_INSTANCE must be 1 to 64 UTF-8 bytes")
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM agent_message_targets
		WHERE instance=? AND project_id=? AND address=? AND role=?`, instance, in.ProjectID, in.Address, in.Role).Scan(&version); err != nil {
		return nil, err
	}
	if !in.Standby {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_message_targets SET enabled=0
			WHERE instance=? AND project_id=? AND address=? AND role=? AND enabled=1`, instance, in.ProjectID, in.Address, in.Role); err != nil {
			return nil, err
		}
	}
	targetID := uuid.NewString()
	enabled := 1
	if in.Standby {
		enabled = 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_targets
		(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,target_secret_cipher,maximum_level,role,enabled,version)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, targetID, instance, in.ProjectID, in.Address, in.Adapter, in.TargetKind,
		ciphertext, secretCipher, in.MaximumLevel, in.Role, enabled, version); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTarget(ctx, in.ProjectID, targetID)
}

func (s *Service) GetTarget(ctx context.Context, projectID int64, targetID string) (*Target, error) {
	var target Target
	if err := scanTarget(s.db.QueryRowContext(ctx, `SELECT `+targetSelectColumns+`
		FROM agent_message_targets WHERE project_id=? AND id=?`, projectID, targetID), &target); err != nil {
		return nil, err
	}
	return &target, nil
}

// TargetRefMatches compares an expected private reference with target
// ciphertext without disclosing the stored value to callers.
func (s *Service) TargetRefMatches(ctx context.Context, projectID int64, targetID, expected string) (bool, error) {
	var cipher []byte
	if err := s.db.QueryRowContext(ctx, `SELECT target_ref_cipher FROM agent_message_targets WHERE project_id=? AND id=?`, projectID, targetID).Scan(&cipher); err != nil {
		return false, err
	}
	plain, err := secretvault.Decrypt(targetSecretDomain, cipher)
	if err != nil {
		return false, fmt.Errorf("decrypt agent message target: %w", err)
	}
	want := []byte(expected)
	if len(plain) != len(want) {
		return false, nil
	}
	return subtle.ConstantTimeCompare(plain, want) == 1, nil
}

func (s *Service) ListTargets(ctx context.Context, projectID int64, address string) ([]Target, error) {
	args := []any{projectID, instanceName()}
	query := `SELECT ` + targetSelectColumns + `
		FROM agent_message_targets WHERE project_id=? AND instance=?`
	if strings.TrimSpace(address) != "" {
		harness, agent, err := parseAddress(address)
		if err != nil {
			return nil, err
		}
		query += ` AND address=?`
		args = append(args, harness+":"+agent)
	}
	query += ` ORDER BY address,role,version DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []Target
	for rows.Next() {
		var target Target
		if err := scanTarget(rows, &target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func resolveTargetVersionsTx(ctx context.Context, tx *sql.Tx, instance string, projectID int64, address string) (string, string, error) {
	var primary, fallback sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT id FROM agent_message_targets WHERE instance=? AND project_id=? AND address=? AND role='primary' AND enabled=1),
		(SELECT id FROM agent_message_targets WHERE instance=? AND project_id=? AND address=? AND role='simple_fallback' AND enabled=1)`,
		instance, projectID, address, instance, projectID, address).Scan(&primary, &fallback)
	if err != nil {
		return "", "", err
	}
	return primary.String, fallback.String, nil
}

// RequeueMissingTargets explicitly attaches the receiver's current target
// versions to never-attempted target_missing deliveries. Target registration
// itself never releases historical rows.
func (s *Service) RequeueMissingTargets(ctx context.Context, projectID int64, address string) (int64, error) {
	harness, agent, err := parseAddress(address)
	if err != nil {
		return 0, err
	}
	address = harness + ":" + agent
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	primary, fallback, err := resolveTargetVersionsTx(ctx, tx, instanceName(), projectID, address)
	if err != nil {
		return 0, err
	}
	if primary == "" && fallback == "" {
		return 0, coded("agent_message_target_missing", "receiver has no enabled delivery target")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET
		primary_target_id=?,fallback_target_id=?,state='pending',fallback_reason='',last_error_code='',
		next_attempt_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE instance=? AND state='blocked' AND fallback_reason='target_missing' AND attempt_count=0
		AND message_row_id IN (SELECT id FROM agent_messages WHERE to_address=? AND from_agent_id IN
		 (SELECT id FROM project_agents WHERE project_id=?))`, nullableString(primary), nullableString(fallback),
		instanceName(), address, projectID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET delivery_primary_target_id=?,delivery_fallback_target_id=?
		WHERE id IN (SELECT message_row_id FROM agent_message_deliveries WHERE instance=? AND state='pending'
		 AND primary_target_id IS ? AND fallback_target_id IS ?)`, nullableString(primary), nullableString(fallback),
		instanceName(), nullableString(primary), nullableString(fallback)); err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// attachDeliveryWork leases one local delivery whose selected target matches
// the worker's adapter (a Codex thread for codex, a Claude session for
// claude_resume or claude_channel) and decrypts only that snapshotted
// receiver-owned reference. Work that belongs to another adapter is returned
// as redacted state for observability and is never leased or disclosed;
// webhook capabilities are never disclosed through listen.
func (s *Service) attachDeliveryWork(ctx context.Context, projectID int64, address, agent, workerAdapter, workerTargetID string, envelope *Envelope) (bool, error) {
	if _, _, err := s.resolveAttributedInbox(ctx, projectID, address, agent); err != nil {
		return false, err
	}
	work := DeliveryWork{Instance: instanceName(), ProjectID: projectID}
	var selectedTargetID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT d.delivery_id,d.state,d.requested_level,d.fallback_reason,`+selectedDeliveryTargetSQL+`
		FROM agent_message_deliveries d JOIN agent_messages am ON am.id=d.message_row_id
		WHERE am.message_id=? AND d.instance=?`, envelope.MessageID, instanceName()).Scan(
		&work.DeliveryID, &work.State, &work.RequestedLevel, &work.FallbackReason, &selectedTargetID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if work.State == "handed_off" {
		return false, nil
	}
	selectedID := selectedTargetID.String
	if selectedID == "" {
		envelope.DeliveryWork = &work
		return true, nil
	}
	var cipher []byte
	if err := s.db.QueryRowContext(ctx, `SELECT adapter,target_kind,target_ref_cipher,maximum_level
		FROM agent_message_targets WHERE id=? AND instance=? AND project_id=? AND address=?`, selectedID,
		instanceName(), projectID, address).Scan(&work.Adapter, &work.TargetKind, &cipher, &work.MaximumLevel); err != nil {
		return false, err
	}
	if work.Adapter != workerAdapter {
		// Another worker (or the server-side webhook dispatcher) owns this
		// target. Return state for observability without ever exposing the
		// reference.
		envelope.DeliveryWork = &work
		return true, nil
	}
	if workerTargetID != "" && selectedID != workerTargetID {
		// A durable managed worker owns exactly its encrypted binding. Do not
		// lease work after an operator rotates the address to another target.
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='leased',attempt_count=attempt_count+1,
		lease_until=strftime('%Y-%m-%dT%H:%M:%fZ','now','+30 seconds'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE delivery_id=? AND ((state IN ('pending','retry') AND next_attempt_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 OR (state='leased' AND lease_until<=strftime('%Y-%m-%dT%H:%M:%fZ','now')))
		AND NOT EXISTS (SELECT 1 FROM agent_message_deliveries older
		 JOIN agent_messages older_message ON older_message.id=older.message_row_id
		 WHERE older.instance=? AND older_message.to_address=?
		 AND older_message.id<(SELECT current_message.id FROM agent_message_deliveries current_delivery
		  JOIN agent_messages current_message ON current_message.id=current_delivery.message_row_id
		  WHERE current_delivery.delivery_id=?) AND older.state NOT IN ('handed_off','dead'))`,
		work.DeliveryID, instanceName(), address, work.DeliveryID)
	if err != nil {
		return false, err
	}
	leased, _ := result.RowsAffected()
	if leased == 0 {
		return false, nil
	}
	plain, err := secretvault.Decrypt(targetSecretDomain, cipher)
	if err != nil {
		return false, fmt.Errorf("decrypt agent message target: %w", err)
	}
	work.State = "leased"
	work.TargetRef = string(plain)
	envelope.DeliveryWork = &work
	return true, nil
}

type CompleteDeliveryInput struct {
	ProjectID      int64
	Address        string
	Agent          string
	Cursor         int64
	DeliveryID     string
	EffectiveLevel string
	FallbackReason string
	TargetID       string
}

type RerouteUnavailableInput struct {
	ProjectID      int64
	Address        string
	Agent          string
	Cursor         int64
	DeliveryID     string
	FallbackReason string
}

type RerouteUnavailableResult struct {
	DeliveryID       string `json:"delivery_id"`
	Route            string `json:"route"`
	TargetID         string `json:"target_id"`
	HarnessSessionID string `json:"harness_session_id,omitempty"`
}

// RerouteUnavailableLocalDelivery releases a failed agentd lease without
// claiming a managed effect. It atomically selects either the one currently
// working, steer-capable M161 generation or the message's snapshotted simple
// fallback. The replacement worker must obtain a fresh canonical lease and
// finish through CompleteLocalDelivery.
func (s *Service) RerouteUnavailableLocalDelivery(ctx context.Context, in RerouteUnavailableInput) (*RerouteUnavailableResult, error) {
	validReason := in.FallbackReason == "idle" || in.FallbackReason == "not_steerable" || in.FallbackReason == "transport_error"
	if !validReason {
		return nil, coded("agent_message_fallback_reason_invalid", "managed reroute requires idle, not_steerable, or transport_error")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	address, _, err := resolveAttributedInboxQuery(ctx, tx, in.ProjectID, in.Address, in.Agent)
	if err != nil {
		return nil, err
	}
	harnessName, agentName, err := parseAddress(address)
	if err != nil {
		return nil, err
	}
	var state, selectedTargetID, selectedAdapter string
	var cursor int64
	var fallbackTargetID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT d.state,am.id,`+selectedDeliveryTargetSQL+`,t.adapter,d.fallback_target_id
		FROM agent_message_deliveries d
		JOIN agent_messages am ON am.id=d.message_row_id
		JOIN agent_message_targets t ON t.id=`+selectedDeliveryTargetSQL+`
		WHERE d.delivery_id=? AND d.instance=? AND am.to_address=?`, strings.TrimSpace(in.DeliveryID), instanceName(), address).Scan(
		&state, &cursor, &selectedTargetID, &selectedAdapter, &fallbackTargetID)
	if err != nil {
		return nil, coded("agent_message_delivery_unknown", "delivery does not belong to this inbox")
	}
	if state != "leased" || cursor != in.Cursor {
		return nil, coded("agent_message_delivery_not_leased", "managed delivery lease is no longer current")
	}
	if !IsManagedAgentdAdapter(selectedAdapter) {
		return nil, coded("agent_message_delivery_adapter_mismatch", "only an unavailable agentd lease can be rerouted")
	}
	blockNoRoute := func(detail string) (*RerouteUnavailableResult, error) {
		result, updateErr := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='blocked',lease_until=NULL,
			last_error_code='managed_target_blocked',fallback_reason='target_missing',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE delivery_id=? AND state='leased'`, strings.TrimSpace(in.DeliveryID))
		if updateErr != nil {
			return nil, updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return nil, coded("agent_message_delivery_raced", "managed delivery lease changed before blocking")
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, coded("agent_message_target_missing", detail)
	}

	var sessionID, generationTargetID string
	err = tx.QueryRowContext(ctx, `SELECT hs.id,hs.message_target_id
		FROM harness_sessions hs JOIN agent_message_targets t ON t.id=hs.message_target_id
		WHERE hs.project_id=? AND hs.harness=? AND hs.agent_name=? AND hs.management_mode='managed'
		AND hs.phase='working' AND hs.steer_mode='owned' AND hs.advertised_steer=1
		AND hs.heartbeat_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
		AND t.instance=? AND t.adapter=? AND t.maximum_level='steer'
		ORDER BY hs.created_at DESC,hs.id DESC LIMIT 1`, in.ProjectID, harnessName, agentName,
		fmt.Sprintf("-%d seconds", int(ManagedGenerationLivenessWindow/time.Second)), instanceName(), AdapterManagedHarness).Scan(&sessionID, &generationTargetID)
	if err == nil {
		result, updateErr := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET primary_target_id=?,state='pending',
			lease_until=NULL,last_error_code='',fallback_reason='',next_attempt_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE delivery_id=? AND state='leased'`,
			generationTargetID, strings.TrimSpace(in.DeliveryID))
		if updateErr != nil {
			return nil, updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return nil, coded("agent_message_delivery_raced", "managed delivery lease changed before reroute")
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &RerouteUnavailableResult{DeliveryID: in.DeliveryID, Route: "active_generation", TargetID: generationTargetID, HarnessSessionID: sessionID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !fallbackTargetID.Valid {
		return blockNoRoute("unavailable managed target has no active generation or simple fallback")
	}
	var fallbackAdapter, fallbackMaximum, fallbackRole string
	if err := tx.QueryRowContext(ctx, `SELECT adapter,maximum_level,role FROM agent_message_targets
		WHERE id=? AND instance=? AND project_id=? AND address=?`, fallbackTargetID.String, instanceName(), in.ProjectID, address).Scan(
		&fallbackAdapter, &fallbackMaximum, &fallbackRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return blockNoRoute("configured simple fallback is unavailable")
		}
		return nil, err
	}
	if fallbackMaximum != "simple" || fallbackRole != "simple_fallback" || IsManagedAgentdAdapter(fallbackAdapter) || !IsLocalWorkerAdapter(fallbackAdapter) {
		return blockNoRoute("configured fallback is not an ordinary simple local target")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='pending',lease_until=NULL,
		last_error_code='managed_target_unavailable',fallback_reason=?,next_attempt_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE delivery_id=? AND state='leased'`, in.FallbackReason, strings.TrimSpace(in.DeliveryID))
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, coded("agent_message_delivery_raced", "managed delivery lease changed before reroute")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &RerouteUnavailableResult{DeliveryID: in.DeliveryID, Route: "simple_fallback", TargetID: fallbackTargetID.String}, nil
}

// IsManagedAgentdAdapter reports whether adapter is an agentd-owned live
// Process target. Bus reroute and managed-harness standby registration share
// this predicate so adding an owned adapter cannot silently change primacy.
func IsManagedAgentdAdapter(adapter string) bool {
	return adapter == AdapterAgentdCodex || adapter == AdapterAgentdClaude
}

// CompleteLocalDelivery records one accepted local primitive (including a
// lease-correlated agentd managed steer) and advances the FIFO inbox cursor in
// the same transaction while preserving effective level, fallback and time.
func (s *Service) CompleteLocalDelivery(ctx context.Context, in CompleteDeliveryInput) (*CursorState, error) {
	if in.EffectiveLevel != "simple" && in.EffectiveLevel != "steer" {
		return nil, coded("agent_message_effective_level_invalid", "effective_level must be simple or steer")
	}
	validReason := map[string]bool{"": true, "idle": true, "unsupported": true, "policy_capped": true, "target_missing": true, "not_steerable": true, "transport_error": true}
	if !validReason[in.FallbackReason] {
		return nil, coded("agent_message_fallback_reason_invalid", "fallback_reason is not recognized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	address, agentID, err := resolveAttributedInboxQuery(ctx, tx, in.ProjectID, in.Address, in.Agent)
	if err != nil {
		return nil, err
	}
	var state string
	var messageCursor int64
	var requestedLevel, maximumLevel, adapter, targetID string
	if err := tx.QueryRowContext(ctx, `SELECT d.state,d.requested_level,t.maximum_level,t.adapter,t.id,am.id
		FROM agent_message_deliveries d
		JOIN agent_messages am ON am.id=d.message_row_id
		JOIN agent_message_targets t ON t.id=`+selectedDeliveryTargetSQL+`
		WHERE d.delivery_id=? AND d.instance=? AND am.to_address=?`, in.DeliveryID, instanceName(), address).Scan(
		&state, &requestedLevel, &maximumLevel, &adapter, &targetID, &messageCursor); err != nil {
		return nil, coded("agent_message_delivery_unknown", "delivery does not belong to this inbox")
	}
	if in.TargetID != "" && targetID != in.TargetID {
		return nil, coded("agent_message_delivery_target_mismatch", "delivery is leased to another encrypted target binding")
	}
	if !IsLocalWorkerAdapter(adapter) {
		return nil, coded("agent_message_delivery_adapter_mismatch", "local completion is available only for local harness adapters")
	}
	plugin, err := harnessplugin.Lookup(adapter)
	if err != nil {
		return nil, coded("agent_message_delivery_adapter_mismatch", "UNSUPPORTED: delivery adapter is not registered")
	}
	if in.EffectiveLevel == "steer" && plugin.MaximumLevel() == "simple" {
		return nil, coded("agent_message_effective_level_invalid", "adapter has no steer primitive; effective_level must be simple")
	}
	if in.EffectiveLevel == "steer" && (requestedLevel != "steer" || maximumLevel != "steer") {
		return nil, coded("agent_message_effective_level_invalid", "steer was not allowed by the durable request and receiver policy")
	}
	if in.Cursor != messageCursor {
		return nil, coded("agent_message_cursor_invalid", "cursor does not match delivery message")
	}
	if state != "handed_off" {
		if state != "leased" {
			return nil, coded("agent_message_delivery_not_leased", "delivery is not leased by a local worker")
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='handed_off',effective_level=?,fallback_reason=?,
			handed_off_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),lease_until=NULL,last_error_code='',
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE delivery_id=? AND state='leased'`,
			in.EffectiveLevel, in.FallbackReason, in.DeliveryID)
		if err != nil {
			return nil, err
		}
		updated, _ := result.RowsAffected()
		if updated != 1 {
			return nil, coded("agent_message_delivery_raced", "delivery lease changed before completion")
		}
	}
	stateOut, err := ackInboxTx(ctx, tx, in.ProjectID, address, agentID, in.Cursor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stateOut, nil
}

// DeliveryStatus is the redacted operator view of outbox state.
type DeliveryStatus struct {
	DeliveryID     string `json:"delivery_id"`
	MessageID      string `json:"message_id"`
	Address        string `json:"address"`
	RequestedLevel string `json:"requested_level"`
	EffectiveLevel string `json:"effective_level,omitempty"`
	State          string `json:"state"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	AttemptCount   int    `json:"attempt_count"`
	LastErrorCode  string `json:"last_error_code,omitempty"`
	HandedOffAt    string `json:"handed_off_at,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

func (s *Service) ListDeliveryStatus(ctx context.Context, projectID int64) ([]DeliveryStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.delivery_id,am.message_id,am.to_address,d.requested_level,
		COALESCE(d.effective_level,''),d.state,d.fallback_reason,d.attempt_count,d.last_error_code,
		COALESCE(d.handed_off_at,''),d.updated_at FROM agent_message_deliveries d
		JOIN agent_messages am ON am.id=d.message_row_id JOIN project_agents pa ON pa.id=am.to_agent_id
		WHERE pa.project_id=? AND d.instance=? ORDER BY am.id`, projectID, instanceName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryStatus
	for rows.Next() {
		var status DeliveryStatus
		if err := rows.Scan(&status.DeliveryID, &status.MessageID, &status.Address, &status.RequestedLevel,
			&status.EffectiveLevel, &status.State, &status.FallbackReason, &status.AttemptCount,
			&status.LastErrorCode, &status.HandedOffAt, &status.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, rows.Err()
}

// RequeueDelivery is an explicit operator recovery for blocked, dead, retry,
// or expired-lease rows. It reuses the stable delivery_id and never creates a
// second canonical message.
func (s *Service) RequeueDelivery(ctx context.Context, projectID int64, deliveryID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='pending',lease_until=NULL,
		last_error_code='',next_attempt_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE delivery_id=? AND instance=? AND state IN ('blocked','dead','retry','leased')
		AND COALESCE(primary_target_id,fallback_target_id) IS NOT NULL
		AND message_row_id IN (SELECT am.id FROM agent_messages am JOIN project_agents pa ON pa.id=am.to_agent_id
		 WHERE pa.project_id=?)`, strings.TrimSpace(deliveryID), instanceName(), projectID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return coded("agent_message_delivery_not_requeueable", "delivery is not a requeueable row in this project")
	}
	return nil
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
)

var ErrMachineNotifierTargetChanged = errors.New("machine notifier target binding changed")

type MachineNotifierBindingInput struct {
	APIKeyID      int64
	ProjectID     int64
	Sender        string
	Address       string
	TargetID      string
	TargetVersion int
}

// CreateMachineNotifierBindingTx validates and stores one immutable notifier
// route. Only server-side simple webhooks are admitted, keeping delivery-time
// target validation inside the transaction that leases the external effect.
func CreateMachineNotifierBindingTx(ctx context.Context, tx *sql.Tx, in MachineNotifierBindingInput) error {
	if tx == nil || in.APIKeyID <= 0 || in.ProjectID <= 0 || in.TargetVersion <= 0 {
		return coded("machine_notifier_binding_invalid", "machine notifier binding is invalid")
	}
	harness, receiverName, err := parseAddress(in.Address)
	if err != nil {
		return err
	}
	address := harness + ":" + receiverName
	senderName := strings.TrimSpace(in.Sender)
	if !addressPart.MatchString(senderName) {
		return coded("machine_notifier_sender_invalid", "machine notifier sender is invalid")
	}
	instance := instanceName()
	if instance == "" {
		return coded("agent_message_instance_invalid", "PAIMOS_AGENT_BUS_INSTANCE must be configured")
	}
	var senderID, receiverID int64
	var activeCredential, activeProject, allowed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys ak JOIN users u ON u.id=ak.user_id
		WHERE ak.id=? AND ak.credential_kind='machine_notifier' AND ak.disabled_at IS NULL
		AND (ak.expires_at IS NULL OR datetime(ak.expires_at)>datetime('now')) AND u.status='active'`, in.APIKeyID).Scan(&activeCredential); err != nil || activeCredential != 1 {
		return coded("machine_notifier_unauthorized", "machine notifier credential is unavailable")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=? AND status='active'`, in.ProjectID).Scan(&activeProject); err != nil || activeProject != 1 {
		return coded("machine_notifier_project_unavailable", "machine notifier project is unavailable")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, in.ProjectID, senderName).Scan(&senderID); err != nil {
		return coded("machine_notifier_sender_unavailable", "machine notifier sender is unavailable")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, in.ProjectID, receiverName).Scan(&receiverID); err != nil {
		return coded("machine_notifier_receiver_unavailable", "machine notifier receiver is unavailable")
	}
	if senderID == receiverID {
		return coded("machine_notifier_binding_invalid", "machine notifier sender and receiver must differ")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_message_allowlist
		WHERE sender_agent_id=? AND receiver_agent_id=?`, senderID, receiverID).Scan(&allowed); err != nil || allowed != 1 {
		return coded("machine_notifier_sender_unavailable", "machine notifier sender is not allowed for the receiver")
	}
	var adapter string
	if err := tx.QueryRowContext(ctx, `SELECT adapter FROM agent_message_targets
		WHERE id=? AND instance=? AND project_id=? AND address=? AND version=?
		AND role='primary' AND enabled=1 AND target_kind='https_webhook' AND maximum_level='simple'`,
		strings.TrimSpace(in.TargetID), instance, in.ProjectID, address, in.TargetVersion).Scan(&adapter); err != nil ||
		!slices.Contains(harnessplugin.Names(harnessplugin.ModeServer, harnessplugin.KindHTTPSWebhook), adapter) {
		return coded("machine_notifier_target_unavailable", "machine notifier target is not the current simple webhook")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO machine_notifier_bindings
		(api_key_id,instance,project_id,sender_agent_id,receiver_agent_id,address,target_id,target_version)
		VALUES(?,?,?,?,?,?,?,?)`, in.APIKeyID, instance, in.ProjectID, senderID, receiverID, address,
		strings.TrimSpace(in.TargetID), in.TargetVersion)
	return err
}

type MachineNotifierReceipt struct {
	MessageID              string `json:"message_id"`
	ProjectID              int64  `json:"project_id"`
	Address                string `json:"address"`
	State                  string `json:"state"`
	EffectiveLevel         string `json:"effective_level"`
	HandedOffAt            string `json:"handed_off_at"`
	EffectiveTargetID      string `json:"effective_target_id"`
	EffectiveTargetVersion int    `json:"effective_target_version"`
}

func (s *Service) MachineNotifierReceipt(ctx context.Context, messageID string, authority NotifierAuthority) (*MachineNotifierReceipt, error) {
	if authority == nil || strings.TrimSpace(messageID) == "" {
		return nil, coded("machine_notifier_receipt_unavailable", "machine notifier receipt is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keyID, err := authority(ctx, tx)
	if err != nil || keyID <= 0 {
		return nil, coded("machine_notifier_unauthorized", "machine notifier credential is unavailable")
	}
	var receipt MachineNotifierReceipt
	err = tx.QueryRowContext(ctx, `SELECT m.message_id,b.project_id,b.address,d.state,
		COALESCE(d.effective_level,''),COALESCE(d.handed_off_at,''),COALESCE(d.primary_target_id,''),COALESCE(t.version,0)
		FROM agent_messages m
		JOIN machine_notifier_bindings b ON b.api_key_id=m.machine_notifier_api_key_id
		JOIN agent_message_deliveries d ON d.message_row_id=m.id AND d.instance=b.instance
		LEFT JOIN agent_message_targets t ON t.id=d.primary_target_id
		WHERE m.message_id=? AND m.machine_notifier_api_key_id=?`, strings.TrimSpace(messageID), keyID).Scan(
		&receipt.MessageID, &receipt.ProjectID, &receipt.Address, &receipt.State, &receipt.EffectiveLevel,
		&receipt.HandedOffAt, &receipt.EffectiveTargetID, &receipt.EffectiveTargetVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, coded("machine_notifier_receipt_unavailable", "machine notifier receipt is unavailable")
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// machineNotifierDeliveryTargetCurrentTx is called immediately before a
// server-side webhook lease commits. A rotated/disabled target never silently
// falls through to a replacement or fallback.
func machineNotifierDeliveryTargetCurrentTx(ctx context.Context, tx *sql.Tx, deliveryID, selectedTargetID string) (bool, error) {
	var keyID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT m.machine_notifier_api_key_id FROM agent_message_deliveries d
		JOIN agent_messages m ON m.id=d.message_row_id WHERE d.delivery_id=?`, deliveryID).Scan(&keyID); err != nil {
		return false, err
	}
	if !keyID.Valid {
		return true, nil
	}
	var adapter string
	err := tx.QueryRowContext(ctx, `SELECT t.adapter FROM machine_notifier_bindings b
		JOIN api_keys ak ON ak.id=b.api_key_id
		JOIN users u ON u.id=ak.user_id
		JOIN projects p ON p.id=b.project_id
		JOIN agent_message_deliveries d ON d.delivery_id=?
		JOIN agent_messages m ON m.id=d.message_row_id AND m.machine_notifier_api_key_id=b.api_key_id
		JOIN agent_message_targets t ON t.id=b.target_id
		WHERE b.api_key_id=? AND d.instance=b.instance
		AND m.from_agent_id=b.sender_agent_id AND m.to_agent_id=b.receiver_agent_id AND m.to_address=b.address
		AND d.primary_target_id=b.target_id AND d.fallback_target_id IS NULL
		AND b.target_id=? AND t.id=? AND t.instance=b.instance AND t.project_id=b.project_id
		AND t.address=b.address AND t.version=b.target_version AND t.role='primary' AND t.enabled=1
		AND t.target_kind='https_webhook' AND t.maximum_level='simple'
		AND ak.credential_kind='machine_notifier' AND ak.disabled_at IS NULL
		AND (ak.expires_at IS NULL OR datetime(ak.expires_at)>datetime('now'))
		AND u.status='active' AND p.status='active'
		AND EXISTS(SELECT 1 FROM agent_message_allowlist allow
		           WHERE allow.sender_agent_id=b.sender_agent_id AND allow.receiver_agent_id=b.receiver_agent_id)`, deliveryID, keyID.Int64,
		selectedTargetID, selectedTargetID).Scan(&adapter)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.Contains(harnessplugin.Names(harnessplugin.ModeServer, harnessplugin.KindHTTPSWebhook), adapter), nil
}

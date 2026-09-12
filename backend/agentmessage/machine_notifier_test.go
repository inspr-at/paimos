// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	paimosdb "github.com/inspr-at/paimos/backend/db"
)

const hostd59NotifierBody = "HOSTD-59 paper Gateway notification. Please SendToUser this concise notice to Markus in this existing Grok chat. This is notification only: do not trade, restart anything, or change account settings. Event test-event: controlled test notice"

type machineNotifierFixture struct {
	service   *Service
	projectID int64
	keyID     int64
	target    *Target
}

func newMachineNotifierFixture(t *testing.T) machineNotifierFixture {
	return newMachineNotifierFixtureAt(t, "https://127.0.0.1/hook")
}

func newMachineNotifierFixtureAt(t *testing.T, targetURL string) machineNotifierFixture {
	t.Helper()
	service, projectID := openBusTestDB(t)
	allowBusSender(t, service, projectID, "grok_bot:amy")
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	target, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: AdapterGrokBotRoutine,
		TargetKind: TargetKindHTTPSWebhook, TargetRef: targetURL,
		TargetSecret: busTestSenderKey, MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?,?,?,?)`,
		"notifier-owner", "disabled", "admin", "active")
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	key, err := paimosdb.DB.Exec(`INSERT INTO api_keys
		(user_id,name,key_hash,key_prefix,scopes,expires_at,credential_kind) VALUES(?,?,?,?,?,?,?)`,
		userID, "notifier", fmt.Sprintf("fixture-hash-%d", projectID), "fixture", "",
		time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05.000Z"), "machine_notifier")
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()
	tx, err := paimosdb.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateMachineNotifierBindingTx(context.Background(), tx, MachineNotifierBindingInput{
		APIKeyID: keyID, ProjectID: projectID, Sender: "sender", Address: "grok_bot:amy",
		TargetID: target.ID, TargetVersion: target.Version,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return machineNotifierFixture{service: service, projectID: projectID, keyID: keyID, target: target}
}

func TestMachineNotifierWebhookHandoffProducesContentFreeReceipt(t *testing.T) {
	var wake webhookWake
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&wake); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	f := newMachineNotifierFixtureAt(t, server.URL)
	messageID := f.send(t, "hostd59-handoff")
	dispatcher := NewWebhookDispatcher(paimosdb.DB)
	dispatcher.client = server.Client()
	worked, err := dispatcher.DispatchOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if wake.MessageID != messageID || wake.To != "grok_bot:amy" || wake.RequestedLevel != "simple" ||
		wake.EffectiveLevel != "simple" || wake.Content == "" {
		t.Fatalf("wake message=%q to=%q requested=%q effective=%q has_content=%v",
			wake.MessageID, wake.To, wake.RequestedLevel, wake.EffectiveLevel, wake.Content != "")
	}
	receipt, err := f.service.MachineNotifierReceipt(context.Background(), messageID,
		func(context.Context, *sql.Tx) (int64, error) { return f.keyID, nil })
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "handed_off" || receipt.EffectiveLevel != "simple" || receipt.HandedOffAt == "" ||
		receipt.EffectiveTargetID != f.target.ID || receipt.EffectiveTargetVersion != f.target.Version {
		t.Fatalf("receipt=%#v", receipt)
	}
}

func (f machineNotifierFixture) send(t *testing.T, idempotency string) string {
	t.Helper()
	message, err := f.service.SendEnvelope(context.Background(), SendEnvelopeInput{
		Body: hostd59NotifierBody, IdempotencyKey: idempotency,
		NotifierAuthority: func(context.Context, *sql.Tx) (int64, error) { return f.keyID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return message.MessageID
}

func TestMachineNotifierBindsHOSTD59MessageAndOwnReceipt(t *testing.T) {
	f := newMachineNotifierFixture(t)
	messageID := f.send(t, "hostd59-test-event")

	var projectID, keyID int64
	var sender, receiver, level, primary string
	var fallback sql.NullString
	if err := paimosdb.DB.QueryRow(`SELECT sender.project_id,m.machine_notifier_api_key_id,sender.name,receiver.name,
		m.delivery_level,d.primary_target_id,d.fallback_target_id
		FROM agent_messages m JOIN project_agents sender ON sender.id=m.from_agent_id
		JOIN project_agents receiver ON receiver.id=m.to_agent_id
		JOIN agent_message_deliveries d ON d.message_row_id=m.id WHERE m.message_id=?`, messageID).Scan(
		&projectID, &keyID, &sender, &receiver, &level, &primary, &fallback); err != nil {
		t.Fatal(err)
	}
	if projectID != f.projectID || keyID != f.keyID || sender != "sender" || receiver != "amy" ||
		level != "simple" || primary != f.target.ID || fallback.Valid {
		t.Fatalf("bound message project=%d key=%d sender=%q receiver=%q level=%q primary=%q fallback=%v",
			projectID, keyID, sender, receiver, level, primary, fallback)
	}
	receipt, err := f.service.MachineNotifierReceipt(context.Background(), messageID,
		func(context.Context, *sql.Tx) (int64, error) { return f.keyID, nil })
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MessageID != messageID || receipt.ProjectID != f.projectID || receipt.Address != "grok_bot:amy" ||
		receipt.State != "pending" || receipt.EffectiveLevel != "" || receipt.HandedOffAt != "" ||
		receipt.EffectiveTargetID != f.target.ID || receipt.EffectiveTargetVersion != f.target.Version {
		t.Fatalf("receipt=%#v", receipt)
	}
	if _, err := f.service.MachineNotifierReceipt(context.Background(), messageID,
		func(context.Context, *sql.Tx) (int64, error) { return f.keyID + 1, nil }); err == nil {
		t.Fatal("foreign notifier read another credential's receipt")
	}
}

func TestMachineNotifierDeliveryRechecksCurrentAuthority(t *testing.T) {
	mutations := map[string]func(*testing.T, machineNotifierFixture){
		"credential revoked": func(t *testing.T, f machineNotifierFixture) {
			_, err := paimosdb.DB.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, f.keyID)
			if err != nil {
				t.Fatal(err)
			}
		},
		"credential expired": func(t *testing.T, f machineNotifierFixture) {
			_, err := paimosdb.DB.Exec(`UPDATE api_keys SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.000Z"), f.keyID)
			if err != nil {
				t.Fatal(err)
			}
		},
		"owner inactive": func(t *testing.T, f machineNotifierFixture) {
			_, err := paimosdb.DB.Exec(`UPDATE users SET status='inactive' WHERE id=(SELECT user_id FROM api_keys WHERE id=?)`, f.keyID)
			if err != nil {
				t.Fatal(err)
			}
		},
		"project archived": func(t *testing.T, f machineNotifierFixture) {
			_, err := paimosdb.DB.Exec(`UPDATE projects SET status='archived' WHERE id=?`, f.projectID)
			if err != nil {
				t.Fatal(err)
			}
		},
		"allowlist revoked": func(t *testing.T, f machineNotifierFixture) {
			_, err := paimosdb.DB.Exec(`DELETE FROM agent_message_allowlist WHERE sender_agent_id=(SELECT sender_agent_id FROM machine_notifier_bindings WHERE api_key_id=?)`, f.keyID)
			if err != nil {
				t.Fatal(err)
			}
		},
		"target disabled": func(t *testing.T, f machineNotifierFixture) {
			_, err := paimosdb.DB.Exec(`UPDATE agent_message_targets SET enabled=0 WHERE id=?`, f.target.ID)
			if err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newMachineNotifierFixture(t)
			f.send(t, "lease-authority-"+name)
			mutate(t, f)
			worked, err := NewWebhookDispatcher(paimosdb.DB).DispatchOne(context.Background())
			if !worked || !errors.Is(err, ErrMachineNotifierTargetChanged) {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			var state, lastError string
			if err := paimosdb.DB.QueryRow(`SELECT state,last_error_code FROM agent_message_deliveries`).Scan(&state, &lastError); err != nil {
				t.Fatal(err)
			}
			if state != "dead" || lastError != "notifier_target_changed" {
				t.Fatalf("state=%q last_error=%q", state, lastError)
			}
		})
	}
}

func TestMachineNotifierIdempotencyDoesNotRerouteAfterTargetReplacement(t *testing.T) {
	f := newMachineNotifierFixture(t)
	messageID := f.send(t, "stable-event")
	if _, err := f.service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: f.projectID, Address: "grok_bot:amy", Adapter: AdapterGrokBotRoutine,
		TargetKind: TargetKindHTTPSWebhook, TargetRef: "https://127.0.0.1/replacement",
		TargetSecret: busTestSenderKey, MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	replayed := f.send(t, "stable-event")
	if replayed != messageID {
		t.Fatalf("replay message=%q want=%q", replayed, messageID)
	}
	var messages, deliveries int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE machine_notifier_api_key_id=?`, f.keyID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || deliveries != 1 {
		t.Fatalf("messages=%d deliveries=%d", messages, deliveries)
	}
	worked, err := NewWebhookDispatcher(paimosdb.DB).DispatchOne(context.Background())
	if !worked || !errors.Is(err, ErrMachineNotifierTargetChanged) {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	receipt, err := f.service.MachineNotifierReceipt(context.Background(), messageID,
		func(context.Context, *sql.Tx) (int64, error) { return f.keyID, nil })
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "dead" || receipt.EffectiveTargetID != f.target.ID ||
		receipt.EffectiveTargetVersion != f.target.Version {
		t.Fatalf("rotated-target receipt=%#v", receipt)
	}
}

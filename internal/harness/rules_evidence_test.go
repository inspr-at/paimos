// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/inspr-at/paimos/internal/harness"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/jackc/pgx/v5"
)

type deliveryFixture struct {
	*harnessFixture
	path, lease string
	prepare     rules.RulesDeliveryPrepare
}

func newDeliveryFixture(t *testing.T) deliveryFixture {
	t.Helper()
	f := fixture(t)
	order, run := stateRun(t, f, f.project, "running", "DELIVERY-10")
	generation, computer, request, account := "delivery-generation", uid(), uid(), uid()
	f.agent.KeyID = f.agent.AuthKeyID
	f.agent.KeyCreatorID = f.person.ID
	f.agent.RunID = run
	f.agent.RunWorkOrderID = order
	f.agent.RunProjectID = f.project
	f.agent.RunComputerID = computer
	f.agent.TransportComputerID = computer
	f.agent.RunDaemonID = "delivery-daemon"
	f.agent.RunGeneration = generation
	f.agent.Scopes = []string{"harness.worker", "harness.read"}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := agentruns.SetActingPersonTx(t.Context(), tx, f.person.TenantID, run, f.person.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,'135791357',repeat('c',64),repeat('c',64),repeat('c',64),'{"platform":"darwin","arch":"arm64"}',repeat('c',64),'redeemed',$3)`, f.person.TenantID, request, f.person.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,content_daemon_generation) VALUES($1,$2,$3,$4,$5,'delivery-daemon',repeat('c',64),$6)`, f.person.TenantID, computer, request, f.agent.ID, f.agent.KeyID, generation); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,last_daemon_generation) VALUES($1,$2,'delivery-account','claude','delivery-daemon',$3,'Delivery fixture',$4)`, f.person.TenantID, account, f.agent.ID, generation); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,daemon_id='delivery-daemon',daemon_generation=$3 WHERE id=$1`, run, account, generation)
		return err
	})
	lease := "delivery-worker-lease-" + uid()
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "delivery-ref-" + uid(), "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship"}, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	content := []byte("Pinned company rules.\n")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var source string
		commit := strings.Repeat("a", 40)
		if err := tx.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,commit_sha,paths,indexed_at) VALUES($1,'fixture/company','public',$2,ARRAY['docs/AGENTS-KERNEL.md'],clock_timestamp()) RETURNING id::text`, f.person.TenantID, commit).Scan(&source); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id,source_id,commit_sha,path,blob_sha,content) VALUES($1,$2,$3,'docs/AGENTS-KERNEL.md',$4,$5)`, f.person.TenantID, source, commit, doctrine.BlobSHA(content), content)
		return err
	})
	w = f.call(f.agent, "POST", path+"/managed-context", map[string]bool{"pinned_files": true}, lease)
	expect(t, w, 200)
	var merged rules.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	repository := rules.RepositoryRules{Commit: strings.Repeat("b", 40), Body: "Committed repository rules.\n"}
	body, err := rules.AssembleManagedPack(merged, repository)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return deliveryFixture{harnessFixture: f, path: path, lease: lease, prepare: rules.RulesDeliveryPrepare{RunID: run, Generation: generation, BodySHA256: hex.EncodeToString(sum[:]), Repository: repository}}
}
func (f deliveryFixture) issue(t *testing.T) rules.RulesDeliveryManifest {
	t.Helper()
	w := f.call(f.agent, "POST", f.path+"/rules-delivery-manifest", f.prepare, f.lease)
	expect(t, w, 200)
	var out rules.RulesDeliveryManifest
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func deliveryAck(manifest rules.RulesDeliveryManifest) rules.RulesDeliveryAck {
	return rules.RulesDeliveryAck{ManifestID: manifest.ID, RunID: manifest.RunID, Generation: manifest.Generation, BodySHA256: manifest.BodySHA256, BridgeSHA256: manifest.BodySHA256, AcknowledgedAt: manifest.IssuedAt}
}
func (f deliveryFixture) rejected(t *testing.T, in rules.RulesDeliveryAck, status int, reason string) {
	t.Helper()
	w := f.call(f.agent, "POST", f.path+"/rules-delivery-evidence", in, f.lease)
	expect(t, w, status)
	if !strings.Contains(w.Body.String(), reason) {
		t.Fatalf("wrong refusal: %s", w.Body.String())
	}
}

// Risk: a plausible client hash could attest bytes other than the served pack.
func TestRulesDeliveryRejectsTamperedEvidence(t *testing.T) {
	f := newDeliveryFixture(t)
	bad := f.prepare
	bad.BodySHA256 = strings.Repeat("0", 64)
	w := f.call(f.agent, "POST", f.path+"/rules-delivery-manifest", bad, f.lease)
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "served manifest hash mismatch") {
		t.Fatal(w.Body.String())
	}
	manifest := f.issue(t)
	for _, bridge := range []bool{false, true} {
		ack := deliveryAck(manifest)
		if bridge {
			ack.BridgeSHA256 = strings.Repeat("0", 64)
		} else {
			ack.BodySHA256 = strings.Repeat("0", 64)
		}
		f.rejected(t, ack, 409, "served manifest hash mismatch")
	}
	w = f.call(f.agent, "GET", f.path+"/rules-delivery-evidence", nil, "")
	expect(t, w, 200)
	if decode(t, w)["evidence"] != nil {
		t.Fatal("rejected write became evidence")
	}
}

// Risk: another tenant, principal or computer could replay an issued manifest.
func TestRulesDeliveryRejectsForeignEvidence(t *testing.T) {
	f := newDeliveryFixture(t)
	manifest := f.issue(t)
	ack := deliveryAck(manifest)
	foreign := f.agent
	foreign.TenantID = f.foreign.TenantID
	expect(t, f.call(foreign, "POST", f.path+"/rules-delivery-evidence", ack, f.lease), 403)
	foreign = f.agent
	foreign.TransportComputerID = uid()
	foreign.RunComputerID = foreign.TransportComputerID
	w := f.call(foreign, "POST", f.path+"/rules-delivery-evidence", ack, f.lease)
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), "computer or generation rejected") {
		t.Fatal(w.Body.String())
	}
	bare := f.agent
	bare.RunID = ""
	bare.RunComputerID = ""
	bare.RunGeneration = ""
	w = f.call(bare, "POST", f.path+"/rules-delivery-evidence", ack, f.lease)
	expect(t, w, 403)
	if !strings.Contains(w.Body.String(), "run binding rejected") {
		t.Fatal(w.Body.String())
	}
	// Knowing a valid manifest and owning the project never grants a person the agentd writer role.
	expect(t, f.call(f.person, "POST", f.path+"/rules-delivery-evidence", ack, f.lease), 403)
}

// Risk: an acknowledgement might be rebound to a successor run or generation.
func TestRulesDeliveryRejectsReboundEvidence(t *testing.T) {
	f := newDeliveryFixture(t)
	manifest := f.issue(t)
	ack := deliveryAck(manifest)
	ack.RunID = uid()
	f.rejected(t, ack, 403, "run binding rejected")
	ack = deliveryAck(manifest)
	ack.Generation = "replacement-generation"
	f.rejected(t, ack, 403, "run binding rejected")
	ack = deliveryAck(manifest)
	ack.ManifestID = uid()
	f.rejected(t, ack, 403, "manifest binding rejected")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_generation='replacement-generation' WHERE id=$1`, manifest.RunID)
		return err
	})
	f.rejected(t, deliveryAck(manifest), 403, "computer or generation rejected")
}

// Risk: expiry must be enforced before even a syntactically identical ack.
// Seed an expired issuance from the DB clock; no sleeps or fixed calendar time.
func TestRulesDeliveryRejectsExpiredEvidence(t *testing.T) {
	f := newDeliveryFixture(t)
	manifest := rules.RulesDeliveryManifest{ID: uid(), RunID: f.prepare.RunID, SessionID: path.Base(f.path), ComputerID: f.agent.TransportComputerID, Generation: f.prepare.Generation, BodySHA256: f.prepare.BodySHA256, Harness: "claude"}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()-interval '6 minutes'`).Scan(&manifest.IssuedAt); err != nil {
			return err
		}
		manifest.ExpiresAt = manifest.IssuedAt.Add(5 * time.Minute)
		raw, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_rules_deliveries(tenant_id,id,session_id,run_id,computer_id,daemon_generation,manifest,request_digest,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, f.person.TenantID, manifest.ID, manifest.SessionID, manifest.RunID, manifest.ComputerID, manifest.Generation, raw, make([]byte, 32), manifest.IssuedAt, manifest.ExpiresAt)
		return err
	})
	f.rejected(t, deliveryAck(manifest), 409, "manifest expired")
}

// Risk: pre-launch issuance and old worker reports must never become sent-byte evidence.
func TestRulesDeliveryAcknowledgementTypeAndLegacyReceipts(t *testing.T) {
	f := newDeliveryFixture(t)
	manifest := f.issue(t)
	w := f.call(f.agent, "GET", f.path+"/rules-delivery-evidence", nil, "")
	expect(t, w, 200)
	if decode(t, w)["evidence"] != nil {
		t.Fatal("issuance masquerades as ack")
	}
	w = f.call(f.agent, "POST", f.path+"/rules-delivery-manifest", f.prepare, f.lease)
	expect(t, w, 200)
	if decode(t, w)["id"] != manifest.ID {
		t.Fatal("issuance retry changed identity")
	}
	ack := deliveryAck(manifest)
	w = f.call(f.agent, "POST", f.path+"/rules-delivery-evidence", ack, f.lease)
	expect(t, w, 200)
	var evidence rules.RulesDeliveryEvidence
	if err := json.Unmarshal(w.Body.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Evidence != rules.RulesDeliveryAcknowledged || evidence.Evidence == "worker_reported_received" || evidence.Manifest.RepositoryCommit != f.prepare.Repository.Commit || len(evidence.Manifest.Pins) != 1 || evidence.Manifest.BodySHA256 != f.prepare.BodySHA256 {
		t.Fatal("wrong evidence type or binding")
	}
	first := w.Body.String()
	w = f.call(f.agent, "POST", f.path+"/rules-delivery-evidence", ack, f.lease)
	expect(t, w, 200)
	if w.Body.String() != first {
		t.Fatal("ack retry changed evidence")
	}
	// A pre-launch request cannot be submitted as an acknowledgement.
	expect(t, f.call(f.agent, "POST", f.path+"/rules-delivery-evidence", f.prepare, f.lease), 400)
	revision, size := int64(0), 42
	receipt := harnessReceiptForDelivery(f, revision, size)
	legacy := receiptResponse(t, f.call(f.agent, "POST", f.path+"/rules-receipts", receipt, f.lease), false)
	if legacy.Evidence == evidence.Evidence {
		t.Fatal("legacy report conflated with byte ack")
	}
	page := decode(t, f.call(f.person, "GET", f.path+"/rules-receipts", nil, ""))
	if len(page["receipts"].([]any)) != 1 || page["receipts"].([]any)[0].(map[string]any)["evidence"] != "worker_reported_received" {
		t.Fatal("legacy history changed")
	}
	expect(t, f.call(f.agent, "POST", f.path+"/stop", map[string]any{"reason": "stopped"}, f.lease), 200)
	expect(t, f.call(f.agent, "POST", f.path+"/rules-delivery-evidence", ack, f.lease), 403)
	w = f.call(f.person, "GET", f.path+"/rules-delivery-evidence", nil, "")
	expect(t, w, 200)
	if w.Body.String() != first {
		t.Fatal("stopped history changed")
	}
}

func harnessReceiptForDelivery(f deliveryFixture, revision int64, size int) harness.RulesReceiptWrite {
	return harness.RulesReceiptWrite{RequestID: uid(), ExpectedRevision: &revision,
		Context:    rules.Context{TenantID: f.agent.TenantID, ProjectID: f.project, PersonID: f.person.ID, AgentID: f.agent.ID, TaskID: f.agent.RunWorkOrderID, Role: "builder", Harness: "claude-code"},
		BodySHA256: strings.Repeat("c", 64), Version: "floor-only", ByteSize: &size, Source: "floor-only"}
}

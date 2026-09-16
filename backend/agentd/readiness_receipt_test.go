// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func receiptFixture(now time.Time) ReadinessReceipt {
	checks := []ReadinessReceiptCheck{}
	for _, id := range []string{"host_kind", "activated_generation", "doctrine_loader", "workspace_isolation", "tool_prerequisites", "paimos_runtime_doctor", "paimos_account"} {
		checks = append(checks, ReadinessReceiptCheck{ID: id, Status: "pass", Reason: "verified"})
	}
	return ReadinessReceipt{ContractVersion: ReadinessContractVersion, IntentID: uuid.NewString(), ProjectID: 440,
		RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), AccountLabel: "chatgpt",
		ProfileID: "codex-sol-high", ProfileVersion: "1", WorkspaceHandle: uuid.NewString(),
		WorkspaceIdentity: strings.Repeat("1", 64), WorkspaceMode: WorkspaceExclusive, BaselineDigest: "sha256:" + strings.Repeat("2", 64),
		HostKind: "macos-home-manager", Status: "ready", NextAction: "none",
		ObservedAt: now.UTC().Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano), Checks: checks}
}

func receiptRequest(in ReadinessReceipt) ReadinessReceiptRequest {
	return ReadinessReceiptRequest{ProjectID: in.ProjectID, RuntimeID: in.RuntimeID, RuntimeGeneration: in.RuntimeGeneration,
		AccountLabel: in.AccountLabel, AccountKey: in.AccountKey, ProfileID: in.ProfileID, ProfileVersion: in.ProfileVersion,
		WorkspaceHandle: in.WorkspaceHandle, WorkspaceIdentity: in.WorkspaceIdentity, WorkspaceMode: in.WorkspaceMode, BaselineDigest: in.BaselineDigest}
}

func TestReadinessReceiptRequiresExactFreshBindingAndRejectsRestartedGeneration(t *testing.T) {
	root := t.TempDir()
	first, err := NewSupervisor(SupervisorConfig{Instance: "receipt-fixture", StateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	receipt := receiptFixture(time.Now())
	receipt.RuntimeGeneration = first.Status().DaemonID
	if err = first.StoreReadinessReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	got, err := first.ReadinessReceipt(receiptRequest(receipt))
	if err != nil || got.IntentID != receipt.IntentID || got.ExpiresAt != receipt.ExpiresAt {
		t.Fatalf("owned receipt error=%v got=%+v", err, got)
	}
	wrong := receiptRequest(receipt)
	wrong.BaselineDigest = "sha256:" + strings.Repeat("3", 64)
	if _, err = first.ReadinessReceipt(wrong); err == nil {
		t.Fatal("mismatched tuple returned readiness evidence")
	}
	otherProject := receipt
	otherProject.ProjectID++
	otherProject.IntentID = uuid.NewString()
	if err = first.StoreReadinessReceipt(otherProject); err != nil {
		t.Fatal(err)
	}
	if got, err = first.ReadinessReceipt(receiptRequest(receipt)); err != nil || got.ProjectID != receipt.ProjectID {
		t.Fatalf("first project receipt was overwritten: got=%+v err=%v", got, err)
	}
	if got, err = first.ReadinessReceipt(receiptRequest(otherProject)); err != nil || got.ProjectID != otherProject.ProjectID {
		t.Fatalf("other project receipt was unavailable: got=%+v err=%v", got, err)
	}
	if err = first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := NewSupervisor(SupervisorConfig{Instance: "receipt-fixture", StateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if _, err = second.ReadinessReceipt(receiptRequest(receipt)); err == nil {
		t.Fatal("receipt from the prior daemon generation was accepted")
	}
}

func TestReadinessReceiptFailsClosedWhenTheDurableEntryExpires(t *testing.T) {
	s, err := NewSupervisor(SupervisorConfig{Instance: "receipt-expiry", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	receipt := receiptFixture(time.Now().Add(-2 * time.Minute))
	receipt.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err = s.receipts.journal.Put(receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadinessReceipt(receiptRequest(receipt)); err == nil {
		t.Fatal("expired local receipt was accepted")
	}
}

func TestStoreReadinessReceiptPrunesExpiredAndSupersededGenerations(t *testing.T) {
	s, err := NewSupervisor(SupervisorConfig{Instance: "receipt-prune", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	old := receiptFixture(time.Now().Add(-2 * time.Minute))
	old.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err = s.receipts.journal.Put(old); err != nil {
		t.Fatal(err)
	}
	fresh := receiptFixture(time.Now())
	fresh.ProjectID = old.ProjectID
	fresh.RuntimeID = old.RuntimeID
	if err = s.StoreReadinessReceipt(fresh); err != nil {
		t.Fatal(err)
	}
	for _, saved := range s.receipts.journal.Snapshot() {
		if saved.RuntimeID == old.RuntimeID && saved.RuntimeGeneration == old.RuntimeGeneration {
			t.Fatal("expired superseded receipt was retained")
		}
	}
}

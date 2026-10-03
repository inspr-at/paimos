// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProviderPendingPollsItsOriginalKeyWithoutAllocatingTwice(t *testing.T) {
	f := newFixture(t)
	f.provider.pendingKind = "backup"
	polls := 0
	f.s.wait = func(context.Context) error { polls++; return nil }
	j := f.prepare(t, f.project)
	if polls != 1 || f.provider.executeCalls[operationKey(j.Identity, "backup")] != 1 {
		t.Fatal("pending operation allocated twice or never polled")
	}
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
}
func TestEveryProviderCrashWindowKeepsReservationUntilConfirmedCleanup(t *testing.T) {
	for _, kind := range []string{"backup", "restore", "pin"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.provider.lostKind = kind
			j := f.claim(t, f.project)
			if err := f.s.runAttempt(t.Context(), f.a, &j, nil); err == nil {
				t.Fatal("unknown effect reported success")
			}
			if err := f.s.fail(t.Context(), f.a, j, "backup_failed", true); err != nil {
				t.Fatal(err)
			}
			if f.count(t, `SELECT reserved_backup_bytes FROM delivery_adoption_jobs`) != 1024 {
				t.Fatal("unknown effect reservation was lost")
			}
			f.provider.lostKind = ""
			if err := f.s.reconcileJob(t.Context(), f.a, f.project); err != nil {
				t.Fatal(err)
			}
			if f.count(t, `SELECT reserved_backup_bytes FROM delivery_adoption_jobs`) != 0 {
				t.Fatal("confirmed cleanup did not reclaim quota")
			}
			if f.provider.executeCalls[operationKey(j.Identity, kind)] != 1 {
				t.Fatal("crashed effect allocated twice")
			}
			if f.count(t, `SELECT count(*) FROM project_delivery`) != 0 {
				t.Fatal("cleanup accidentally adopted")
			}
		})
	}
}
func TestProviderEvidenceMustProveIdentityIntegrityRestoreAndQuota(t *testing.T) {
	for _, which := range []string{"tenant", "digest", "chain", "restore", "fingerprint", "size"} {
		t.Run(which, func(t *testing.T) {
			f := newFixture(t)
			j := f.claim(t, f.project)
			f.provider.alter = func(v *ProviderResult) {
				switch which {
				case "tenant":
					v.Identity.Tenant = newUUID()
				case "digest":
					v.BackupDigest = "invalid"
				case "chain":
					v.ChainVerified = false
				case "restore":
					v.Restored = false
				case "fingerprint":
					v.Fingerprint = sum([]byte("different source"))
				case "size":
					v.Bytes = 1025
				}
			}
			err := f.s.runAttempt(t.Context(), f.a, &j, nil)
			if err == nil {
				t.Fatal("invalid recovery proof adopted")
			}
			if f.count(t, `SELECT count(*) FROM project_delivery`) != 0 {
				t.Fatal("invalid evidence switched mode")
			}
			if which != "tenant" {
				var failure *failure
				if !errors.As(err, &failure) || failure.code != "backup_failed" && failure.code != "backup_capacity" {
					t.Fatalf("wrong refusal: %v", err)
				}
			}
		})
	}
}
func TestProviderNativeExpiryReclaimsScratchButRetainsSuccessfulRecovery(t *testing.T) {
	f := newFixture(t)
	j := f.prepare(t, f.project)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(31 * time.Minute)
	restore, _ := f.provider.Lookup(t.Context(), Operation{Key: operationKey(j.Identity, "restore")})
	if restore.State != "reclaimed" {
		t.Fatal("provider did not enforce restore hard expiry")
	}
	f.clock = f.clock.Add(25 * time.Hour)
	backup, _ := f.provider.Lookup(t.Context(), Operation{Key: operationKey(j.Identity, "backup")})
	pin, _ := f.provider.Lookup(t.Context(), Operation{Key: operationKey(j.Identity, "pin")})
	if backup.State != "complete" || !backup.Protected || pin.State != "complete" || !pin.Protected {
		t.Fatal("successful recovery chain expired with scratch")
	}
	// A failed, unpinned attempt expires without relying on the application.
	other := f.node(t, "project", "PR-2", "", "open")
	failed := f.claim(t, other)
	f.provider.lostKind = "backup"
	if err := f.s.runAttempt(t.Context(), f.a, &failed, nil); err == nil {
		t.Fatal("fixture did not fail after acquisition")
	}
	f.clock = f.clock.Add(25 * time.Hour)
	payload, _ := f.provider.Lookup(t.Context(), Operation{Key: operationKey(failed.Identity, "backup")})
	if payload.State != "reclaimed" {
		t.Fatal("failed unpinned payload exceeded retention bound")
	}
}
func TestExternalCatalogRetainsPinsAfterDatabaseCheckpointLoss(t *testing.T) {
	f := newFixture(t)
	j := f.prepare(t, f.project)
	f.sql(t, `UPDATE delivery_adoption_jobs SET state='pending',lease_token=NULL,lease_until=NULL,operation_journal='{"operations":[]}',resource_attempt_id=NULL,reserved_backup_bytes=0,reserved_restore_slots=0,backup_verified_at=NULL,evidence_attempt_id=NULL,backup_ref=NULL,backup_digest=NULL,restore_evidence_ref=NULL,restore_evidence_digest=NULL,recovery_pin_manifest_ref=NULL WHERE project_node_id=$1`, f.project)
	if err := f.s.reconcileCatalog(t.Context()); err == nil {
		t.Fatal("unknown external recovery pin was silently accepted")
	}
	pin, _ := f.provider.Lookup(t.Context(), Operation{Key: operationKey(j.Identity, "pin")})
	if !pin.Protected || pin.State != "complete" {
		t.Fatal("database restore deleted external recovery pin")
	}
	if f.count(t, `SELECT count(*) FROM delivery_adoption_jobs WHERE reason_code='recovery_unknown' AND cleanup_state='blocked'`) != 1 {
		t.Fatal("recovery ambiguity was not persisted")
	}
	if err := f.s.reconcileJob(t.Context(), f.a, f.project); err == nil {
		t.Fatal("ambiguous restore catalog authorized cleanup")
	}
}

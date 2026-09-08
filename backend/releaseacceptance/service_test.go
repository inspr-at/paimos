// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/mailer"
)

func TestMintIsIdempotentAndBindsArtifact(t *testing.T) {
	f := openFixture(t)
	first := f.mint()
	second := f.mint()
	if first.Release.ID != second.Release.ID || first.Release.ArtifactDigest != testArt {
		t.Fatalf("mint identity drifted: %+v %+v", first.Release, second.Release)
	}
	if first.Release.State != StateBuilt {
		t.Fatalf("minted state=%s", first.Release.State)
	}
}

func TestMintRequiresHumanEditor(t *testing.T) {
	f := openFixture(t)
	key := Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.adminID, APIKeyID: 9}
	if _, err := f.svc.Mint(context.Background(), key, f.projectID, f.batchID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("api key mint err=%v", err)
	}
	imp := f.admin
	imp.Impersonated = true
	if _, err := f.svc.Mint(context.Background(), imp, f.projectID, f.batchID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("impersonated mint err=%v", err)
	}
	if _, err := f.svc.Mint(context.Background(), f.viewer, f.projectID, f.batchID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer mint err=%v", err)
	}
}

func TestMintWithoutArtifactFails(t *testing.T) {
	f := openFixture(t)
	f.svc.Artifacts = stubArtifacts{err: fmtErr("built receipt artifact is missing")}
	if _, err := f.svc.Mint(context.Background(), f.admin, f.projectID, f.batchID); err == nil {
		t.Fatal("expected missing artifact")
	}
}

func fmtErr(s string) error { return errors.New("release_acceptance_conflict: " + s) }

func TestConfigureAndConfirmBoundaries(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	acc := f.configure(rel.Release.ID, ModeAgencySupported)
	if acc.OperatingModeLabel != "agency-supported" || acc.Defaults.AgreementRef != "SOW-9" {
		t.Fatalf("defaults/label: %+v", acc)
	}
	if !strings.Contains(acc.OfferDisclaimer, "not an offer that Augmentoring will operate") {
		t.Fatalf("disclaimer=%q", acc.OfferDisclaimer)
	}
	if _, err := f.svc.Confirm(context.Background(), f.delivery, f.projectID, rel.Release.ID, "party_customer", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong party err=%v", err)
	}
	if _, err := f.svc.Confirm(context.Background(), f.viewer, f.projectID, rel.Release.ID, "party_customer", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlinked viewer err=%v", err)
	}
	if _, err := f.svc.Confirm(context.Background(), f.customer, f.projectID, rel.Release.ID, "party_customer", ""); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRevisionDoesNotCarryConfirmations(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	acc := f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.Confirm(context.Background(), f.customer, f.projectID, rel.Release.ID, "party_customer", ""); err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Configure(context.Background(), f.admin, f.projectID, rel.Release.ID, ConfigureRequest{
		ExpectedRevision:  acc.Revision,
		OperatingMode:     ModeAgencyOperated,
		AgreementRef:      "SOW-9",
		DisclosedGaps:     []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		DeliveryPartyRef:  "party_delivery",
		OperatorPartyRef:  "party_customer",
		RequiredPartyRefs: []string{"party_delivery", "party_customer"},
		Parties: []PartyInput{
			{PartyRef: "party_customer", Kind: PartyLinkedUser, UserID: f.customerID, Email: "acc-customer@example.test", DisplayName: "Customer", Roles: []string{"acceptance_party", "operator"}},
			{PartyRef: "party_delivery", Kind: PartyLinkedUser, UserID: f.deliveryID, Email: "acc-delivery@example.test", DisplayName: "Delivery", Roles: []string{"acceptance_party", "delivery_party"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.OperatingModeLabel != "provider-operated" {
		t.Fatalf("label=%s", next.OperatingModeLabel)
	}
	if len(next.Confirmations) != 0 {
		t.Fatalf("stale confirmations carried: %+v", next.Confirmations)
	}
}

func TestRevokedSessionCannotConfirm(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeCustomerOperated)
	if _, err := appdb.DB.Exec(`UPDATE sessions SET expires_at=datetime('now','-1 hour') WHERE credential_id=?`, f.customer.SessionCredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Confirm(context.Background(), f.customer, f.projectID, rel.Release.ID, "party_customer", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked session err=%v", err)
	}
}

func TestImmutableReleaseIdentity(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	_, err := appdb.DB.Exec(`UPDATE release_records SET artifact_digest=? WHERE id=?`, testDigest, rel.Release.ID)
	if err == nil {
		t.Fatal("identity mutated")
	}
}

func TestConcurrentConfirmIsIdempotent(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Confirm(context.Background(), f.customer, f.projectID, rel.Release.ID, "party_customer", "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Confirmations) != 1 {
		t.Fatalf("confirmations=%d", len(got.Confirmations))
	}
}

func TestUnconfiguredSMTPNeverMarksSent(t *testing.T) {
	f := openFixture(t)
	f.svc.Mail = mailer.Unconfigured{}
	rel := f.mint()
	acc := f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "Accept ACC", Body: "Please accept release " + acc.Release.ReleaseRef}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "send-1", RecipientPartyRefs: []string{"party_customer", "party_delivery"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusAccepted {
		t.Fatal("unconfigured SMTP produced acceptance")
	}
	if len(got.EmailEvidence) != 1 || got.EmailEvidence[0].State == MailSent {
		t.Fatalf("evidence=%+v", got.EmailEvidence)
	}
}

func TestFakeMailSendAndManualPathFinalize(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.Confirm(context.Background(), f.customer, f.projectID, rel.Release.ID, "party_customer", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Confirm(context.Background(), f.delivery, f.projectID, rel.Release.ID, "party_delivery", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "Accept", Body: "short acceptance among involved parties"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if got.Status == StatusAccepted {
		t.Fatal("confirmations without email finalized")
	}
	got, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "send-ok", RecipientPartyRefs: []string{"party_customer", "party_delivery"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusAccepted || got.Release.State != StateAccepted {
		t.Fatalf("expected accepted got %+v missing=%+v", got.Status, got.Missing)
	}
	if len(f.mail.messages) != 1 {
		t.Fatalf("sent=%d", len(f.mail.messages))
	}
}

func TestManualExternalEmailPath(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeCustomerOperated)
	raw := "From: a@example.test\r\nTo: acc-customer@example.test, acc-delivery@example.test\r\nSubject: Accept\r\n\r\nWe accept.\r\n"
	got, err := f.svc.RecordExternal(context.Background(), f.admin, f.projectID, rel.Release.ID, RecordExternalRequest{
		RequestKey: "ext-1", RecipientPartyRefs: []string{"party_customer", "party_delivery"},
		RawMessage: raw, Attestation: "I attest this is the received acceptance email; it does not verify sender identity.",
		AttestedPartyRefs: []string{"party_customer", "party_delivery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusAccepted {
		t.Fatalf("manual path status=%s missing=%+v", got.Status, got.Missing)
	}
	if got.Confirmations[0].Source != SourceExternalEmail || got.Confirmations[0].ActorUserID != f.adminID {
		t.Fatalf("attestation attributed incorrectly: %+v", got.Confirmations)
	}
}

func TestRemoteHTMLImportRejected(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeCustomerOperated)
	_, err := f.svc.RecordExternal(context.Background(), f.admin, f.projectID, rel.Release.ID, RecordExternalRequest{
		RequestKey: "ext-html", RecipientPartyRefs: []string{"party_customer"},
		RawMessage:  "<html><img src=\"https://evil.example/x.png\"></html>",
		Attestation: "nope",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestStandingPolicyDriftAndRevocation(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeCustomerOperated)
	policy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, PolicyRequest{
		PolicyRef: "policy_acc", ContentDigest: testDigest, RevisionSeal: testSeal,
		Parties: []string{"party_customer"}, AgreementRef: "SOW-9",
		Gaps:       []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse: "Same approved baseline implementation updates only.", ExpiresAt: "2026-12-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RevokePolicy(context.Background(), f.admin, f.projectID, policy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked apply err=%v", err)
	}
}

func TestAgencyModeIgnoresStandingPolicy(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencyOperated)
	policy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, PolicyRequest{
		PolicyRef: "policy_agency", ContentDigest: testDigest, RevisionSeal: testSeal,
		Parties: []string{"party_customer"}, AgreementRef: "SOW-9",
		Gaps:       []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse: "no", ExpiresAt: "2026-12-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agency apply err=%v", err)
	}
}

func TestExportEscapesHTML(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{
		Subject: "Accept <script>", Body: "<img src=\"https://x\"> and text",
	}); err != nil {
		t.Fatal(err)
	}
	ct, body, err := f.svc.Export(context.Background(), f.admin, f.projectID, rel.Release.ID, "html")
	if err != nil || !strings.Contains(ct, "text/html") {
		t.Fatalf("export %s %v", ct, err)
	}
	html := string(body)
	if strings.Contains(html, "<script>") || strings.Contains(html, "src=\"https://x\"") {
		t.Fatalf("unescaped html: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("missing escape: %s", html)
	}
}

func TestAuthorizeSendRequiresExplicitConfirm(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	_, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "no", RecipientPartyRefs: []string{"party_customer"}, PreviewRevision: got.PreviewRevision,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestRetryableMailStaysPending(t *testing.T) {
	f := openFixture(t)
	f.mail.err = context.DeadlineExceeded
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	got, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "retry", RecipientPartyRefs: []string{"party_customer", "party_delivery"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EmailEvidence) != 1 || got.EmailEvidence[0].State == MailSent {
		t.Fatalf("retryable marked sent: %+v", got.EmailEvidence)
	}
}

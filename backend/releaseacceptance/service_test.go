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
		AttestedPartyRefs: []string{"party_customer", "party_delivery"}, ConfirmAttest: true,
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
	envID := f.insertProjectEnv("production")
	bound := f.bindProjectEnv(rel.Release.ID, envID)
	policy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, f.standingPolicy("policy_acc", bound.DeploymentTarget))
	if err != nil {
		t.Fatal(err)
	}
	if policy.TargetRef != bound.DeploymentTarget || policy.ModelRef != ModeCustomerOperated {
		t.Fatalf("policy bound wrong scope: %+v", policy)
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
	envID := f.insertProjectEnv("production")
	bound := f.bindProjectEnv(rel.Release.ID, envID)
	policy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, f.standingPolicy("policy_agency", bound.DeploymentTarget, func(req *PolicyRequest) {
		req.BoundedUse = "no"
	}))
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
	if strings.Contains(html, "party_customer") || strings.Contains(html, "party_delivery") {
		t.Fatalf("printable missing still leaks storage refs: %s", html)
	}
	if !strings.Contains(html, "Customer") || !strings.Contains(html, "Delivery") {
		t.Fatalf("printable missing omitted party names: %s", html)
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

func TestAmbiguousMailFailsClosedWithoutRetry(t *testing.T) {
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
	if got.Status == StatusAccepted {
		t.Fatal("ambiguous mail finalized")
	}
	if len(got.EmailEvidence) != 1 || got.EmailEvidence[0].State != MailFailed || got.EmailEvidence[0].LastErrorClass != "smtp_ambiguous" {
		t.Fatalf("ambiguous evidence=%+v", got.EmailEvidence)
	}
	if got.MailRecovery == "" {
		t.Fatal("missing mail recovery")
	}
	if err := f.svc.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.messages) != 0 {
		t.Fatalf("automatic retry after ambiguous delivery: %d", len(f.mail.messages))
	}
	if !got.MailInFlight {
		t.Fatal("ambiguous delivery must block further authorize")
	}
	if _, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "retry-2", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second authorize after ambiguous err=%v", err)
	}
}

func TestQueuedSendHonorsStaleRevision(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	acc := f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	raw := []byte("Subject: A\r\n\r\nB\r\n")
	res, err := appdb.DB.Exec(`INSERT INTO acceptance_email_evidence(
		acceptance_id,release_id,acceptance_revision,message_ref,request_key,recipient_party_refs_json,state,source,
		recorded_at,actor_user_id,session_credential_id,attestation,body_sha256,raw_message)
		VALUES(?,?,?,'message_stale','stale-1','["party_customer"]','pending','platform_send',?,?,?,'','deadbeef',?)`,
		got.ID, rel.Release.ID, got.Revision, f.svc.now(), f.adminID, f.admin.SessionCredentialID, raw)
	if err != nil {
		t.Fatal(err)
	}
	evID, _ := res.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO acceptance_mail_outbox(
		evidence_id,request_key,state,attempt_count,last_error_class,authorized_by,session_credential_id,preview_revision,created_at,updated_at)
		VALUES(?,'stale-1','queued',0,'',?,?,?,?,?)`, evID, f.adminID, f.admin.SessionCredentialID, got.PreviewRevision, f.svc.now(), f.svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Configure(context.Background(), f.admin, f.projectID, rel.Release.ID, ConfigureRequest{
		ExpectedRevision: acc.Revision, OperatingMode: ModeCustomerOperated, AgreementRef: "SOW-9",
		DisclosedGaps:    []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		DeliveryPartyRef: "party_delivery", OperatorPartyRef: "party_customer",
		RequiredPartyRefs: []string{"party_delivery", "party_customer"},
		Parties: []PartyInput{
			{PartyRef: "party_customer", Kind: PartyLinkedUser, UserID: f.customerID, Email: "acc-customer@example.test", DisplayName: "Customer", Roles: []string{"acceptance_party", "operator"}},
			{PartyRef: "party_delivery", Kind: PartyLinkedUser, UserID: f.deliveryID, Email: "acc-delivery@example.test", DisplayName: "Delivery", Roles: []string{"acceptance_party", "delivery_party"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.messages) != 0 {
		t.Fatal("stale queued send delivered")
	}
	out, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.EmailEvidence) != 0 {
		t.Fatalf("stale revision evidence should not count on new revision: %+v", out.EmailEvidence)
	}
	var state, class string
	if err := appdb.DB.QueryRow(`SELECT state,last_error_class FROM acceptance_mail_outbox WHERE request_key='stale-1'`).Scan(&state, &class); err != nil {
		t.Fatal(err)
	}
	if state != MailFailed || class != "smtp_stale" {
		t.Fatalf("stale outbox state=%s class=%s", state, class)
	}
}

func TestQueuedSendHonorsRevokedSession(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	raw := []byte("Subject: A\r\n\r\nB\r\n")
	res, err := appdb.DB.Exec(`INSERT INTO acceptance_email_evidence(
		acceptance_id,release_id,acceptance_revision,message_ref,request_key,recipient_party_refs_json,state,source,
		recorded_at,actor_user_id,session_credential_id,attestation,body_sha256,raw_message)
		VALUES(?,?,?,'message_revoked','revoked-1','["party_customer"]','pending','platform_send',?,?,?,'','deadbeef',?)`,
		got.ID, rel.Release.ID, got.Revision, f.svc.now(), f.adminID, f.admin.SessionCredentialID, raw)
	if err != nil {
		t.Fatal(err)
	}
	evID, _ := res.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO acceptance_mail_outbox(
		evidence_id,request_key,state,attempt_count,last_error_class,authorized_by,session_credential_id,preview_revision,created_at,updated_at)
		VALUES(?,'revoked-1','queued',0,'',?,?,?,?,?)`, evID, f.adminID, f.admin.SessionCredentialID, got.PreviewRevision, f.svc.now(), f.svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := appdb.DB.Exec(`UPDATE sessions SET expires_at=datetime('now','-1 hour') WHERE credential_id=?`, f.admin.SessionCredentialID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.messages) != 0 {
		t.Fatal("revoked queued send delivered")
	}
	out, err := f.svc.Get(context.Background(), f.delivery, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status == StatusAccepted {
		t.Fatal("revoked send finalized")
	}
	if len(out.EmailEvidence) != 1 || out.EmailEvidence[0].State != MailFailed || out.EmailEvidence[0].LastErrorClass != "smtp_revoked" {
		t.Fatalf("revoked evidence=%+v", out.EmailEvidence)
	}
	if out.MailRecovery == "" {
		t.Fatal("missing recovery after revoked send")
	}
}

func TestRecordExternalDoesNotAttestFromRecipients(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	raw := "From: a@example.test\r\nTo: acc-customer@example.test\r\nSubject: Accept\r\n\r\nWe accept.\r\n"
	got, err := f.svc.RecordExternal(context.Background(), f.admin, f.projectID, rel.Release.ID, RecordExternalRequest{
		RequestKey: "ext-no-attest", RecipientPartyRefs: []string{"party_customer", "party_delivery"},
		RawMessage: raw, Attestation: "I recorded this email. Recipients are not consent.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Confirmations) != 0 {
		t.Fatalf("recipients auto-attested: %+v", got.Confirmations)
	}
	if got.Status == StatusAccepted {
		t.Fatal("unattested recorded email finalized")
	}
	_, err = f.svc.RecordExternal(context.Background(), f.admin, f.projectID, rel.Release.ID, RecordExternalRequest{
		RequestKey: "ext-attest-required", RecipientPartyRefs: []string{"party_customer"},
		RawMessage: raw + "x", Attestation: "same recorder", AttestedPartyRefs: []string{"party_customer"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing confirm_attest err=%v", err)
	}
}

func TestDisplayNameChangeBumpsRevision(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	acc := f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.Confirm(context.Background(), f.customer, f.projectID, rel.Release.ID, "party_customer", ""); err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Configure(context.Background(), f.admin, f.projectID, rel.Release.ID, ConfigureRequest{
		ExpectedRevision:  acc.Revision,
		OperatingMode:     ModeAgencySupported,
		AgreementRef:      "SOW-9",
		DisclosedGaps:     []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		DeliveryPartyRef:  "party_delivery",
		OperatorPartyRef:  "party_customer",
		RequiredPartyRefs: []string{"party_delivery", "party_customer"},
		Parties: []PartyInput{
			{PartyRef: "party_customer", Kind: PartyLinkedUser, UserID: f.customerID, Email: "acc-customer@example.test", DisplayName: "Customer GmbH", Roles: []string{"acceptance_party", "operator"}},
			{PartyRef: "party_delivery", Kind: PartyLinkedUser, UserID: f.deliveryID, Email: "acc-delivery@example.test", DisplayName: "Delivery", Roles: []string{"acceptance_party", "delivery_party"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision <= acc.Revision {
		t.Fatalf("display_name edit did not bump revision: %d", next.Revision)
	}
	if len(next.Confirmations) != 0 {
		t.Fatalf("stale confirmation after rename: %+v", next.Confirmations)
	}
	var name string
	if err := appdb.DB.QueryRow(`SELECT display_name FROM acceptance_parties WHERE acceptance_id=? AND acceptance_revision=? AND party_ref='party_customer'`, next.ID, next.Revision).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Customer GmbH" {
		t.Fatalf("display_name=%q", name)
	}
}

func TestCRLFEmailRejected(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	req := ConfigureRequest{
		OperatingMode: ModeAgencySupported, AgreementRef: "SOW-9",
		DisclosedGaps:    []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		DeliveryPartyRef: "party_delivery", OperatorPartyRef: "party_customer",
		RequiredPartyRefs: []string{"party_delivery", "party_customer"},
		Parties: []PartyInput{
			{PartyRef: "party_customer", Kind: PartyManualEmail, Email: "evil@example.test\r\nBcc: hidden@example.test", DisplayName: "Evil", Roles: []string{"acceptance_party", "operator"}},
			{PartyRef: "party_delivery", Kind: PartyLinkedUser, UserID: f.deliveryID, Email: "acc-delivery@example.test", DisplayName: "Delivery", Roles: []string{"acceptance_party", "delivery_party"}},
		},
	}
	if _, err := f.svc.Configure(context.Background(), f.admin, f.projectID, rel.Release.ID, req); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CRLF email err=%v", err)
	}
}

func TestPolicyExpiryAndBindings(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeCustomerOperated)
	if _, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, PolicyRequest{
		PolicyRef: "policy_bad_date", ContentDigest: testDigest, RevisionSeal: testSeal,
		Parties: []string{"party_customer"}, AgreementRef: "SOW-9",
		Gaps:       []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse: "Same approved baseline implementation updates only.", ExpiresAt: "2026-9-1",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("loose expiry err=%v", err)
	}
	if _, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, PolicyRequest{
		PolicyRef: "policy_expired", ContentDigest: testDigest, RevisionSeal: testSeal,
		Parties: []string{"party_customer"}, AgreementRef: "SOW-9",
		Gaps:       []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse: "Same approved baseline implementation updates only.", ExpiresAt: "2020-01-01T00:00:00Z",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired approve err=%v", err)
	}
	envID := f.insertProjectEnvDest("production", "https://old-target.example.invalid", "qa-old-target", "192.0.2.10")
	got, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeploymentTarget != "" {
		t.Fatalf("sole environment auto-bound: %+v", got)
	}
	if len(got.TargetCandidates) != 1 || got.TargetCandidates[0].EnvironmentID != envID {
		t.Fatalf("candidates=%+v", got.TargetCandidates)
	}
	if _, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, PolicyRequest{
		PolicyRef: "policy_name_as_target", ContentDigest: testDigest, RevisionSeal: testSeal,
		Parties: []string{"party_customer"}, AgreementRef: "SOW-9",
		Gaps:       []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse: "Same approved baseline implementation updates only.", ExpiresAt: "2026-12-01T00:00:00Z",
		TargetRef: "production",
		ModelRef:  ModeCustomerOperated,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("environment name as target err=%v", err)
	}
	if _, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, PolicyRequest{
		PolicyRef: "policy_artifact_as_target", ContentDigest: testDigest, RevisionSeal: testSeal,
		Parties: []string{"party_customer"}, AgreementRef: "SOW-9",
		Gaps:       []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse: "Same approved baseline implementation updates only.", ExpiresAt: "2026-12-01T00:00:00Z",
		TargetRef: "ghcr:inspr-at/demo:acc",
		ModelRef:  ModeCustomerOperated,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unverified target err=%v", err)
	}
	bound := f.bindProjectEnv(rel.Release.ID, envID)
	okPolicy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, f.standingPolicy("policy_ok_bind", bound.DeploymentTarget))
	if err != nil {
		t.Fatal(err)
	}
	if okPolicy.TargetRef != bound.DeploymentTarget || okPolicy.ModelRef != ModeCustomerOperated {
		t.Fatalf("bound policy=%+v", okPolicy)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, okPolicy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := appdb.DB.Exec(`UPDATE project_environments SET url=?, host_alias=?, host_ip=?, updated_at=datetime('now') WHERE id=?`,
		"https://new-target.example.invalid", "qa-new-target", "192.0.2.20", envID); err != nil {
		t.Fatal(err)
	}
	moved, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.DeploymentTarget != "" {
		t.Fatalf("destination PUT kept bound digest: %q", moved.DeploymentTarget)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, okPolicy.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("apply after destination PUT err=%v", err)
	}
	if _, err := appdb.DB.Exec(`DELETE FROM project_environments WHERE id=?`, envID); err != nil {
		t.Fatal(err)
	}
	replacement := f.insertProjectEnv("production")
	drifted, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.DeploymentTarget != "" || drifted.TargetUnknownReason == "" {
		t.Fatalf("same-name retarget kept consent: %+v", drifted)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, okPolicy.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("apply after same-name retarget err=%v", err)
	}
	rebound := f.bindProjectEnv(rel.Release.ID, replacement)
	if rebound.DeploymentTarget == bound.DeploymentTarget {
		t.Fatal("replacement environment reused prior identity")
	}
	if _, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, f.standingPolicy("policy_stale_digest", bound.DeploymentTarget)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old identity still approved err=%v", err)
	}
}

func TestExpiredSendingIsAmbiguousWithoutResend(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	res, err := appdb.DB.Exec(`INSERT INTO acceptance_email_evidence(
		acceptance_id,release_id,acceptance_revision,message_ref,request_key,recipient_party_refs_json,state,source,
		recorded_at,actor_user_id,session_credential_id,attestation,body_sha256,raw_message)
		VALUES(?,?,?,'message_inflight','inflight-1','["party_customer"]','pending','platform_send',?,?,?,'','deadbeef',?)`,
		got.ID, rel.Release.ID, got.Revision, f.svc.now(), f.adminID, f.admin.SessionCredentialID, []byte("Subject: A\r\n\r\nB\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	evID, _ := res.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO acceptance_mail_outbox(
		evidence_id,request_key,state,attempt_count,last_error_class,authorized_by,session_credential_id,preview_revision,lease_until,created_at,updated_at)
		VALUES(?,'inflight-1','sending',1,'',?,?,?,'2020-01-01T00:00:00Z',?,?)`, evID, f.adminID, f.admin.SessionCredentialID, got.PreviewRevision, f.svc.now(), f.svc.now()); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.messages) != 0 {
		t.Fatal("expired sending was retried")
	}
	out, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.EmailEvidence) != 1 || out.EmailEvidence[0].DisplayState != MailAmbiguous {
		t.Fatalf("expired sending=%+v", out.EmailEvidence)
	}
	if !strings.Contains(out.MailRecovery, "Do not send this message again") {
		t.Fatalf("recovery invites resend: %q", out.MailRecovery)
	}
	if !out.MailInFlight {
		t.Fatal("expired sending must be treated as in-flight")
	}
}

func TestAmbiguousRecoveryDominatesPriorReject(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	f.mail.err = mailer.ErrRejected
	got, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "reject-first", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.MailRecovery, "authorize a new send") {
		t.Fatalf("rejected recovery=%q", got.MailRecovery)
	}
	f.mail.err = mailer.ErrAmbiguous
	got, err = f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "ambiguous-second", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.MailRecovery, "Do not send this message again") {
		t.Fatalf("ambiguous did not dominate: %q", got.MailRecovery)
	}
	if !got.MailInFlight {
		t.Fatal("ambiguous must disable further authorize")
	}
	if _, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "ambiguous-third", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("third authorize err=%v", err)
	}
}

func TestExportMarksDeliveryState(t *testing.T) {
	f := openFixture(t)
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	f.mail.err = mailer.ErrRejected
	if _, err := f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "export-fail", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, body, err := f.svc.Export(context.Background(), f.admin, f.projectID, rel.Release.ID, "eml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "X-Paimos-Delivery-State: failed") {
		t.Fatalf("eml missing delivery state: %s", body)
	}
}

func TestPharosOwnerGraphBindRevokeAndAuthority(t *testing.T) {
	f := openFixture(t)
	owner := f.seedPharosOwner("production")
	rel := f.mintBatch(owner.BatchID)
	f.configure(rel.Release.ID, ModeCustomerOperated)
	envID := f.insertProjectEnv("production")
	got, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeploymentTarget != "" {
		t.Fatalf("pharos existence auto-bound: %+v", got)
	}
	foundPharos := false
	for _, c := range got.TargetCandidates {
		if c.Kind == TargetKindPharosOwner && c.RegistrationID == owner.RegistrationID {
			foundPharos = true
		}
	}
	if !foundPharos {
		t.Fatalf("live pharos owner missing from candidates: %+v", got.TargetCandidates)
	}

	wrong := f.seedPharosOwner("staging")
	if _, err := f.svc.BindTarget(context.Background(), f.admin, f.projectID, rel.Release.ID, BindTargetRequest{
		Kind: TargetKindPharosOwner, RegistrationID: wrong.RegistrationID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong delivery bind err=%v", err)
	}

	bound := f.bindPharos(rel.Release.ID, owner.RegistrationID)
	if bound.DeploymentTargetLabel != "" && strings.Contains(strings.ToLower(bound.DeploymentTargetLabel), "http") {
		t.Fatalf("portal label leaked destination: %q", bound.DeploymentTargetLabel)
	}
	policy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, f.standingPolicy("policy_pharos", bound.DeploymentTarget))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); err != nil {
		t.Fatal(err)
	}

	f.revokePharos(owner.DeliveryKey, owner.RegistrationID)
	afterRevoke, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRevoke.DeploymentTarget != "" {
		t.Fatalf("revoked pharos fell back to project env: %+v", afterRevoke)
	}
	if !strings.Contains(afterRevoke.TargetUnknownReason, "cannot fall back") {
		t.Fatalf("reason=%q", afterRevoke.TargetUnknownReason)
	}
	if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("apply after revoke err=%v", err)
	}
	_ = envID
}

func TestPharosAuthorityLossInvalidatesPolicy(t *testing.T) {
	cases := []struct {
		name string
		lose func(*fixture, pharosOwner)
	}{
		{"disabled key", func(f *fixture, o pharosOwner) {
			if _, err := appdb.DB.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, o.APIKeyID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"expired key", func(f *fixture, o pharosOwner) {
			if _, err := appdb.DB.Exec(`UPDATE api_keys SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 hour') WHERE id=?`, o.APIKeyID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"owner role", func(f *fixture, o pharosOwner) {
			if _, err := appdb.DB.Exec(`UPDATE users SET role='external' WHERE id=?`, o.UserID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"membership", func(f *fixture, o pharosOwner) {
			if _, err := appdb.DB.Exec(`UPDATE project_members SET access_level='none' WHERE project_id=? AND user_id=?`, f.projectID, o.UserID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"archived user", func(f *fixture, o pharosOwner) {
			if _, err := appdb.DB.Exec(`UPDATE users SET status='archived' WHERE id=?`, o.UserID); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := openFixture(t)
			owner := f.seedPharosOwner("production")
			rel := f.mintBatch(owner.BatchID)
			f.configure(rel.Release.ID, ModeCustomerOperated)
			bound := f.bindPharos(rel.Release.ID, owner.RegistrationID)
			policy, err := f.svc.ApprovePolicy(context.Background(), f.admin, f.projectID, f.standingPolicy("policy_"+strings.ReplaceAll(tc.name, " ", "_"), bound.DeploymentTarget))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); err != nil {
				t.Fatal(err)
			}
			tc.lose(f, owner)
			got, err := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.DeploymentTarget != "" {
				t.Fatalf("%s kept bound target: %+v", tc.name, got)
			}
			if _, err := f.svc.ApplyPolicy(context.Background(), f.customer, f.projectID, rel.Release.ID, policy.ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("%s apply err=%v", tc.name, err)
			}
		})
	}
}

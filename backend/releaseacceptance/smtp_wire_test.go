// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"context"
	"mime"
	"strings"
	"sync"
	"testing"
	"time"

	appdb 	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/mailer"
	"github.com/inspr-at/paimos/backend/mailer/smtptest"
)

func TestLoopbackSMTPWireAndAmbiguousDATA(t *testing.T) {
	okServer, err := smtptest.Start(smtptest.OK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(okServer.Close)
	host, port := smtptest.SplitHostPort(okServer.Addr)
	f := openFixture(t)
	f.svc.Mail = mailer.SMTP{Host: host, Port: port, From: "paimos@example.test"}
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	subject := "Freigabe für Änderung"
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{
		Subject: subject, Body: "Bitte die gebaute Version prüfen.",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	got, err = f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "wire-1", RecipientPartyRefs: []string{"party_customer", "party_delivery"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailEvidence[0].State != MailSent {
		t.Fatalf("wire send state=%+v", got.EmailEvidence[0])
	}
	msgs := okServer.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages=%d", len(msgs))
	}
	raw := string(msgs[0])
	if !strings.Contains(raw, "From:") || !strings.Contains(raw, "paimos@example.test") || !strings.Contains(raw, "Date:") || !strings.Contains(raw, "Message-ID:") {
		t.Fatalf("missing rfc5322 headers: %s", raw)
	}
	encoded := mime.QEncoding.Encode("utf-8", subject)
	if strings.Contains(raw, "Content-Transfer-Encoding: 8bit") {
		t.Fatalf("8bit without 8BITMIME: %s", raw)
	}
	if !strings.Contains(raw, encoded) || !strings.Contains(raw, "Content-Transfer-Encoding: quoted-printable") || !strings.Contains(raw, "pr=C3=BCfen") {
		t.Fatalf("unicode payload missing: %s", raw)
	}

	hang, err := smtptest.Start(smtptest.HangAfterDATA, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hang.Close)
	hHost, hPort := smtptest.SplitHostPort(hang.Addr)
	f.svc.Mail = mailer.SMTP{Host: hHost, Port: hPort, From: "paimos@example.test"}
	f.svc.SendTimeout = 400 * time.Millisecond
	f.svc.Lease = time.Hour
	got, err = f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "wire-hang", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailEvidence[len(got.EmailEvidence)-1].DisplayState != MailAmbiguous {
		t.Fatalf("timeout after DATA=%+v", got.EmailEvidence)
	}
	if hang.DATACount() != 1 {
		t.Fatalf("hang data count=%d", hang.DATACount())
	}
	if err := f.svc.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hang.DATACount() != 1 {
		t.Fatalf("ambiguous delivery retried: %d", hang.DATACount())
	}
}

func TestParallelDrainDoesNotDoubleSend(t *testing.T) {
	slow, err := smtptest.Start(smtptest.SlowAfterDATA, 600*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slow.Close)
	host, port := smtptest.SplitHostPort(slow.Addr)
	f := openFixture(t)
	f.svc.Clock = ClockFunc(time.Now)
	f.svc.Mail = mailer.SMTP{Host: host, Port: port, From: "paimos@example.test"}
	f.svc.Lease = 150 * time.Millisecond
	f.svc.SendTimeout = 3 * time.Second
	rel := f.mint()
	acc := f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	raw := buildRawMessage(got, []string{"party_customer"}, "paimos@example.test", time.Now().UTC(), "parallel")
	res, err := appdb.DB.Exec(`INSERT INTO acceptance_email_evidence(
		acceptance_id,release_id,acceptance_revision,message_ref,request_key,recipient_party_refs_json,state,source,
		recorded_at,actor_user_id,session_credential_id,attestation,body_sha256,raw_message)
		VALUES(?,?,?,'message_parallel','parallel-1','["party_customer"]','pending','platform_send',?,?,?,'','deadbeef',?)`,
		got.ID, rel.Release.ID, acc.Revision, f.svc.now(), f.adminID, f.admin.SessionCredentialID, raw)
	if err != nil {
		t.Fatal(err)
	}
	evID, _ := res.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO acceptance_mail_outbox(
		evidence_id,request_key,state,attempt_count,last_error_class,authorized_by,session_credential_id,preview_revision,created_at,updated_at)
		VALUES(?,'parallel-1','queued',0,'',?,?,?,?,?)`, evID, f.adminID, f.admin.SessionCredentialID, got.PreviewRevision, f.svc.now(), f.svc.now()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.svc.DrainOnce(context.Background())
		}()
	}
	wg.Wait()
	if slow.DATACount() != 1 {
		t.Fatalf("parallel drains sent %d times", slow.DATACount())
	}
}

func TestSMTPRejectBeforeDATAIsRejected(t *testing.T) {
	srv, err := smtptest.Start(smtptest.RejectMAIL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	host, port := smtptest.SplitHostPort(srv.Addr)
	f := openFixture(t)
	f.svc.Mail = mailer.SMTP{Host: host, Port: port, From: "paimos@example.test"}
	rel := f.mint()
	f.configure(rel.Release.ID, ModeAgencySupported)
	if _, err := f.svc.SavePreview(context.Background(), f.admin, f.projectID, rel.Release.ID, PreviewRequest{Subject: "A", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), f.admin, f.projectID, rel.Release.ID)
	got, err = f.svc.AuthorizeSend(context.Background(), f.admin, f.projectID, rel.Release.ID, AuthorizeSendRequest{
		RequestKey: "reject-mail", RecipientPartyRefs: []string{"party_customer"},
		PreviewRevision: got.PreviewRevision, ConfirmSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := got.EmailEvidence[0]
	if ev.DisplayState != MailFailed || ev.LastErrorClass != "smtp_rejected" {
		t.Fatalf("pre-send reject=%+v", ev)
	}
	if strings.Contains(got.MailRecovery, "Do not send this message again") {
		t.Fatalf("pre-send treated as ambiguous: %q", got.MailRecovery)
	}
	if srv.DATACount() != 0 {
		t.Fatal("DATA issued after MAIL reject")
	}
}

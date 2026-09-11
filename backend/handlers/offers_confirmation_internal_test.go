// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/internal/testdb"
	"github.com/inspr-at/paimos/backend/mailer"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

type offerMailSink struct {
	calls    int
	messages []mailer.Message
	err      error
}

func (s *offerMailSink) SenderAddress() string { return "from@example.test" }
func (s *offerMailSink) Send(_ context.Context, m mailer.Message) error {
	s.calls++
	s.messages = append(s.messages, m)
	return s.err
}
func confirmationFixture(t *testing.T) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close(); db.DB = nil })
	doc := OfferDocument{Title: "Consulting", Customer: OfferCustomer{Name: "Kunde", Email: "kunde@example.test"}, Sender: OfferSender{Company: "Consulting GmbH", Email: "office@example.test"}}
	raw, _ := json.Marshal(doc)
	for _, q := range []string{`INSERT INTO customers(id,name) VALUES(1,'Kunde')`, `INSERT INTO offers(id,offer_no,customer_id,status,document,accepted_at,accepted_name,accepted_company,accepted_note) VALUES(1,'A260911-01',1,'accepted','` + string(raw) + `','2026-09-11T12:00:00Z','Eva Test','Kunde','Alles klar')`, `INSERT INTO offer_acceptance_audit VALUES(1,'2026-09-11T12:00:00Z','Eva Test','Kunde','Alles klar','192.0.2.1','Private browser','` + strings.Repeat("a", 64) + `',2)`, `INSERT INTO offer_confirmations(offer_id,next_attempt_at,updated_at,public_url,message_id) VALUES(1,'2020-01-01','2020-01-01','https://example.test/offers/test','test-message')`} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}
func TestOfferConfirmationAttachmentAndIdempotency(t *testing.T) {
	confirmationFixture(t)
	sink := &offerMailSink{}
	renders := 0
	pdf := []byte("%PDF-test attachment")
	render := func(_ context.Context, payload any, _ string) ([]byte, error) {
		renders++
		o := payload.(publicOffer)
		if o.DocumentSHA256 != strings.Repeat("a", 64) || o.AcceptedName != "Eva Test" {
			t.Fatal("receipt missing from PDF")
		}
		return pdf, nil
	}
	for range 2 {
		if err := dispatchOfferConfirmation(context.Background(), sink, render); err != nil {
			t.Fatal(err)
		}
	}
	if sink.calls != 1 || renders != 1 {
		t.Fatal("duplicate delivery")
	}
	message, err := mail.ReadMessage(bytes.NewReader(sink.messages[0].Raw))
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := message.Header.AddressList("To")
	if err != nil || len(addresses) != 2 {
		t.Fatal("both recipients must be in To")
	}
	_, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	reader := multipart.NewReader(message.Body, params["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
	for _, value := range []string{"Eva Test", "Alles klar", "Europe/Vienna", "14:00:00", "Annahmenachweis (SHA-256):", "https://example.test/offers/test"} {
		if !strings.Contains(string(body), value) {
			t.Fatalf("missing %s", value)
		}
	}
	if strings.Contains(string(body), "192.0.2.1") || strings.Contains(string(body), "Private browser") {
		t.Fatal("private audit leaked")
	}
	attachment, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
	if !bytes.Equal(data, pdf) {
		t.Fatal("attachment mismatch")
	}
	var state string
	var stored []byte
	if err = db.DB.QueryRow(`SELECT state,pdf FROM offer_confirmations`).Scan(&state, &stored); err != nil || state != "sent" || !bytes.Equal(pdf, stored) {
		t.Fatal("stored PDF mismatch")
	}
}
func TestOfferConfirmationFailureRetryAndAmbiguity(t *testing.T) {
	confirmationFixture(t)
	sink := &offerMailSink{}
	render := func(context.Context, any, string) ([]byte, error) { return nil, errors.New("fixture failure") }
	if err := dispatchOfferConfirmation(context.Background(), sink, render); err != nil {
		t.Fatal(err)
	}
	if sink.calls != 0 {
		t.Fatal("mail before PDF")
	}
	var state, status string
	db.DB.QueryRow(`SELECT state FROM offer_confirmations`).Scan(&state)
	db.DB.QueryRow(`SELECT status FROM offers`).Scan(&status)
	if state != "pending" || status != "accepted" {
		t.Fatal("failure changed acceptance")
	}
	db.DB.Exec(`UPDATE offer_confirmations SET next_attempt_at='2020-01-01'`)
	render = func(context.Context, any, string) ([]byte, error) { return []byte("%PDF-test"), nil }
	sink.err = mailer.ErrAmbiguous
	for range 2 {
		if err := dispatchOfferConfirmation(context.Background(), sink, render); err != nil {
			t.Fatal(err)
		}
	}
	db.DB.QueryRow(`SELECT state FROM offer_confirmations`).Scan(&state)
	if state != "uncertain" || sink.calls != 1 {
		t.Fatal("ambiguous send retried")
	}
	db.DB.Exec(`UPDATE offer_confirmations SET state='sending',updated_at=?`, time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339))
	dispatchOfferConfirmation(context.Background(), sink, render)
	db.DB.QueryRow(`SELECT state FROM offer_confirmations`).Scan(&state)
	if state != "uncertain" || sink.calls != 1 {
		t.Fatal("crash recovery resent uncertain mail")
	}
}
func TestOfferConfirmationRecipientDeduplication(t *testing.T) {
	o := Offer{Document: OfferDocument{Customer: OfferCustomer{Email: "Office@example.test"}, Sender: OfferSender{Email: "office@example.test"}}}
	msg, err := offerConfirmationMessage(o, "https://example.test", "id", []byte("%PDF-test"), "from@example.test")
	if err != nil || len(msg.To) != 1 {
		t.Fatal("duplicate recipient")
	}
	o.Document.Customer.Email = "bad\r\nBcc: injected@example.test"
	if _, err = offerConfirmationMessage(o, "", "id", nil, "from@example.test"); err == nil {
		t.Fatal("header injection accepted")
	}
}

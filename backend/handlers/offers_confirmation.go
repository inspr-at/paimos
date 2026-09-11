// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/mailer"
	"github.com/inspr-at/paimos/backend/offerpdf"
	"github.com/inspr-at/paimos/backend/publicbase"
)

type OfferConfirmation struct {
	State      string  `json:"state"`
	SentAt     *string `json:"sent_at,omitempty"`
	ErrorClass string  `json:"error_class,omitempty"`
	PDFReady   bool    `json:"pdf_ready"`
}

func validOfferEmail(value string) bool {
	a, err := mail.ParseAddress(value)
	return err == nil && a.Address == value && !strings.ContainsAny(value, "\r\n") && strings.Contains(value, "@")
}
func offerCustomerURL(token string) string {
	base := strings.TrimRight(brand.Default.PublicURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	// The operator-owned origin/prefix is authoritative; never trust an acceptance
	// request Host or forwarding header when constructing an emailed capability.
	u.Path = publicbase.Current().Join(strings.TrimRight(u.Path, "/") + "/offers/" + token)
	return u.String()
}
func loadOfferConfirmation(ctx context.Context, o *Offer) {
	if o.Status != "accepted" {
		return
	}
	_ = db.DB.QueryRowContext(ctx, `SELECT document_sha256 FROM offer_acceptance_audit WHERE offer_id=?`, o.ID).Scan(&o.DocumentSHA256)
	c := &OfferConfirmation{}
	err := db.DB.QueryRowContext(ctx, `SELECT state,sent_at,error_class,pdf IS NOT NULL FROM offer_confirmations WHERE offer_id=?`, o.ID).Scan(&c.State, &c.SentAt, &c.ErrorClass, &c.PDFReady)
	if errors.Is(err, sql.ErrNoRows) {
		c.State = "legacy"
	} else if err != nil {
		c.State = "unavailable"
	}
	o.Confirmation = c
}
func GetOfferDeliveryReadiness(w http.ResponseWriter, r *http.Request) {
	_, unconfigured := mailer.FromEnv().(mailer.Unconfigured)
	jsonOK(w, map[string]bool{"smtp_configured": !unconfigured && validOfferEmail(mailer.SenderAddress(mailer.FromEnv())), "pdf_available": offerpdf.Available(), "public_url_configured": offerCustomerURL("readiness") != ""})
}
func ListOfferSummary(w http.ResponseWriter, r *http.Request) {
	rows, err := db.DB.QueryContext(r.Context(), `SELECT id,customer_id,offer_no,json_extract(document,'$.title'),CASE WHEN status='sent' AND (date(json_extract(document,'$.valid_until')) IS NULL OR json_extract(document,'$.valid_until')<?) THEN 'expired' ELSE status END,EXISTS(SELECT 1 FROM offer_visibility WHERE offer_id=offers.id AND deleted_at IS NOT NULL) FROM offers WHERE (? OR NOT EXISTS(SELECT 1 FROM offer_visibility WHERE offer_id=offers.id AND deleted_at IS NOT NULL)) ORDER BY id DESC`, offerToday(), r.URL.Query().Get("include_deleted") == "1")
	if err != nil {
		jsonError(w, "Angebote konnten nicht geladen werden", 503)
		return
	}
	defer rows.Close()
	type summary struct {
		Deleted    bool   `json:"deleted"`
		ID         int64  `json:"id"`
		CustomerID int64  `json:"customer_id"`
		OfferNo    string `json:"offer_no"`
		Title      string `json:"title"`
		Status     string `json:"status"`
	}
	out := []summary{}
	for rows.Next() {
		var s summary
		if rows.Scan(&s.ID, &s.CustomerID, &s.OfferNo, &s.Title, &s.Status, &s.Deleted) != nil {
			jsonError(w, "Angebote konnten nicht geladen werden", 503)
			return
		}
		out = append(out, s)
	}
	if rows.Err() != nil {
		jsonError(w, "Angebote konnten nicht geladen werden", 503)
		return
	}
	jsonOK(w, out)
}
func RetryOfferConfirmation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AcknowledgeUncertain bool `json:"acknowledge_uncertain"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
		jsonError(w, "Ungültige Anfrage", 400)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := db.DB.ExecContext(r.Context(), `UPDATE offer_confirmations SET state='pending',attempts=0,next_attempt_at=?,updated_at=?,error_class='' WHERE offer_id=? AND (state='failed' OR (state='uncertain' AND ?))`, now, now, chi.URLParam(r, "id"), body.AcknowledgeUncertain)
	if err != nil {
		jsonError(w, "Versand konnte nicht vorgemerkt werden", 503)
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		jsonError(w, "Kein fehlgeschlagener Versand; unklaren Versand bitte ausdrücklich bestätigen", 409)
		return
	}
	jsonOK(w, map[string]string{"state": "pending"})
}
func GetPublicOfferPDF(w http.ResponseWriter, r *http.Request) {
	o, err := loadPublicOffer(r)
	if err != nil || o.Status != "accepted" {
		jsonError(w, "PDF nicht verfügbar", 404)
		return
	}
	writeAcceptedOfferPDF(w, r, o)
}
func GetOfferPDF(w http.ResponseWriter, r *http.Request) {
	o, err := scanOffer(db.DB.QueryRowContext(r.Context(), `SELECT `+offerColumns+` FROM offers WHERE id=?`, chi.URLParam(r, "id")))
	if err != nil || o.Status != "accepted" {
		jsonError(w, "PDF nicht verfügbar", 404)
		return
	}
	writeAcceptedOfferPDF(w, r, o)
}
func writeAcceptedOfferPDF(w http.ResponseWriter, r *http.Request, o Offer) {
	var pdf []byte
	if db.DB.QueryRowContext(r.Context(), `SELECT pdf FROM offer_confirmations WHERE offer_id=? AND pdf IS NOT NULL`, o.ID).Scan(&pdf) != nil {
		w.Header().Set("Retry-After", "5")
		jsonError(w, "Die angenommene PDF wird vorbereitet oder ist für dieses ältere Angebot nicht verfügbar", 409)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writePDFBytesResponse(w, pdf, o.OfferNo+"-angenommen.pdf")
}

func StartOfferConfirmationDispatcher(sender mailer.Mailer) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			_ = dispatchOfferConfirmation(context.Background(), sender, offerpdf.Render)
		}
	}()
}

type offerPDFRenderer func(context.Context, any, string) ([]byte, error)

func dispatchOfferConfirmation(ctx context.Context, sender mailer.Mailer, render offerPDFRenderer) error {
	now := time.Now().UTC()
	cutoff := now.Add(-2 * time.Minute).Format(time.RFC3339)
	// Rendering is safe to repeat. A crash after SMTP DATA is ambiguous, so never
	// auto-resend a stale sending job. Manual retry acknowledges possible delivery.
	_, err := db.DB.ExecContext(ctx, `UPDATE offer_confirmations SET state=CASE WHEN state='sending' THEN 'uncertain' WHEN attempts>=5 THEN 'failed' ELSE 'pending' END,error_class=CASE state WHEN 'sending' THEN 'smtp_ambiguous' ELSE 'pdf_interrupted' END,next_attempt_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','+'||(attempts*attempts*20)||' seconds'),updated_at=? WHERE state IN ('rendering','sending') AND updated_at<?`, now.Format(time.RFC3339), cutoff)
	if err != nil {
		return err
	}
	var id int64
	var attempts int
	var link, messageID string
	var pdf []byte
	err = db.DB.QueryRowContext(ctx, `UPDATE offer_confirmations SET state='rendering',attempts=attempts+1,updated_at=? WHERE offer_id=(SELECT offer_id FROM offer_confirmations WHERE state='pending' AND next_attempt_at<=? ORDER BY next_attempt_at LIMIT 1) AND state='pending' RETURNING offer_id,attempts,public_url,message_id,pdf`, now.Format(time.RFC3339), now.Format(time.RFC3339)).Scan(&id, &attempts, &link, &messageID, &pdf)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(class string, uncertain bool) error {
		state := "pending"
		if attempts >= 5 {
			state = "failed"
		}
		if uncertain {
			state = "uncertain"
		}
		next := time.Now().UTC().Add(time.Duration(attempts*attempts) * 20 * time.Second)
		_, e := db.DB.ExecContext(ctx, `UPDATE offer_confirmations SET state=?,error_class=?,next_attempt_at=?,updated_at=? WHERE offer_id=? AND attempts=? AND state IN ('rendering','sending')`, state, class, next.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), id, attempts)
		return e
	}
	o, err := scanOffer(db.DB.QueryRowContext(ctx, `SELECT `+offerColumns+` FROM offers WHERE id=? AND status='accepted'`, id))
	if err != nil {
		return fail("receipt_unavailable", false)
	}
	loadOfferConfirmation(ctx, &o)
	if len(o.DocumentSHA256) != 64 {
		return fail("receipt_unavailable", false)
	}
	if link == "" {
		link = offerCustomerURL(o.PublicToken)
		if link == "" {
			return fail("public_url_unconfigured", false)
		}
		if _, err = db.DB.ExecContext(ctx, `UPDATE offer_confirmations SET public_url=? WHERE offer_id=? AND state='rendering' AND attempts=?`, link, id, attempts); err != nil {
			return err
		}
	}
	if len(pdf) == 0 {
		pdf, err = render(ctx, publicOfferView(o), link)
		if err != nil || len(pdf) > 20<<20 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
			return fail("pdf_generation_failed", false)
		}
		if _, err = db.DB.ExecContext(ctx, `UPDATE offer_confirmations SET pdf=? WHERE offer_id=? AND state='rendering' AND attempts=? AND pdf IS NULL`, pdf, id, attempts); err != nil {
			return err
		}
	}
	msg, err := offerConfirmationMessage(o, link, messageID, pdf, mailer.SenderAddress(sender))
	if err != nil {
		return fail("message_invalid", false)
	}
	result, err := db.DB.ExecContext(ctx, `UPDATE offer_confirmations SET state='sending',updated_at=? WHERE offer_id=? AND state='rendering' AND attempts=?`, time.Now().UTC().Format(time.RFC3339), id, attempts)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = sender.Send(sendCtx, msg)
	cancel()
	if err != nil {
		return fail(mailer.ErrorClass(err), mailer.ErrorClass(err) == "smtp_ambiguous")
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	_, err = db.DB.ExecContext(ctx, `UPDATE offer_confirmations SET state='sent',sent_at=?,updated_at=?,error_class='' WHERE offer_id=? AND state='sending' AND attempts=?`, stamp, stamp, id, attempts)
	return err
}
func offerConfirmationMessage(o Offer, link, id string, pdf []byte, from string) (mailer.Message, error) {
	if !validOfferEmail(from) {
		return mailer.Message{}, errors.New("invalid sender")
	}
	recipients := []string{o.Document.Customer.Email}
	if !strings.EqualFold(o.Document.Customer.Email, o.Document.Sender.Email) {
		recipients = append(recipients, o.Document.Sender.Email)
	}
	for _, address := range recipients {
		if !validOfferEmail(address) {
			return mailer.Message{}, errors.New("invalid recipient")
		}
	}
	accepted := ""
	if o.AcceptedAt != nil {
		if t, err := time.Parse(time.RFC3339, *o.AcceptedAt); err == nil {
			accepted = t.In(offerLocation).Format("02.01.2006 15:04:05 MST") + " (Europe/Vienna)"
		}
	}
	body := fmt.Sprintf("Angebot %s wurde digital angenommen.\n\nTitel: %s\nAuftraggeber: %s\nAuftragnehmer: %s\nAngenommen von: %s · %s\nZeitpunkt: %s\n", o.OfferNo, o.Document.Title, o.Document.Customer.Name, o.Document.Sender.Company, o.AcceptedName, o.AcceptedCompany, accepted)
	if o.AcceptedNote != "" {
		body += "Anmerkung: " + o.AcceptedNote + "\n"
	}
	body += "\nAnnahmenachweis (SHA-256): " + o.DocumentSHA256 + "\n\nKundenlink: " + link + "\n\nDie angenommene Fassung finden Sie im PDF-Anhang.\n"
	var out bytes.Buffer
	multi := multipart.NewWriter(&out)
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n", from, strings.Join(recipients, ", "), mime.QEncoding.Encode("utf-8", "Angebot "+o.OfferNo+" angenommen"), time.Now().UTC().Format(time.RFC1123Z), id+"@"+strings.Split(from, "@")[1], multi.Boundary())
	writePart := func(contentType, disposition string, data []byte) error {
		h := textproto.MIMEHeader{"Content-Type": {contentType}, "Content-Transfer-Encoding": {"base64"}}
		if disposition != "" {
			h.Set("Content-Disposition", disposition)
		}
		part, e := multi.CreatePart(h)
		if e != nil {
			return e
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		for len(encoded) > 0 {
			n := min(76, len(encoded))
			if _, e = fmt.Fprint(part, encoded[:n]+"\r\n"); e != nil {
				return e
			}
			encoded = encoded[n:]
		}
		return nil
	}
	if err := writePart("text/plain; charset=utf-8", "", []byte(body)); err != nil {
		return mailer.Message{}, err
	}
	if err := writePart("application/pdf", mime.FormatMediaType("attachment", map[string]string{"filename": o.OfferNo + "-angenommen.pdf"}), pdf); err != nil {
		return mailer.Message{}, err
	}
	if err := multi.Close(); err != nil {
		return mailer.Message{}, err
	}
	return mailer.Message{From: from, To: recipients, Subject: "Angebot " + o.OfferNo + " angenommen", Raw: out.Bytes()}, nil
}

// Visibility lives separately because accepted offers and their receipts stay immutable.
func SetOfferDeleted(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Deleted bool `json:"deleted"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil {
		jsonError(w, "Ungültige Anfrage", 400)
		return
	}
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "Anmeldung erforderlich", 401)
		return
	}
	var id int64
	if db.DB.QueryRowContext(r.Context(), `SELECT id FROM offers WHERE id=?`, chi.URLParam(r, "id")).Scan(&id) != nil {
		jsonError(w, "Angebot nicht gefunden", 404)
		return
	}
	var deletedAt any
	if body.Deleted {
		deletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := db.DB.ExecContext(r.Context(), `INSERT INTO offer_visibility(offer_id,deleted_at,deleted_by) VALUES(?,?,?) ON CONFLICT(offer_id) DO UPDATE SET deleted_at=excluded.deleted_at,deleted_by=excluded.deleted_by`, id, deletedAt, user.ID)
	if err != nil {
		jsonError(w, "Änderung konnte nicht gespeichert werden", 503)
		return
	}
	jsonOK(w, map[string]bool{"deleted": body.Deleted})
}

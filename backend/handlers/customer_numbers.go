// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/db"
)

var legacyCustomerNumber = regexp.MustCompile(`^K[0-9]{2}-[0-9]{3,}$`)

func nextCustomerNumber(tx *sql.Tx, now time.Time) (string, error) {
	month := now.In(offerLocation).Format("0601")
	n, err := nextOfferSequence(tx, "customer:"+month)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("K%s%d", month, n), nil
}

// ReformatCustomerNumber converts a legacy number only before any offer for
// this customer is finalized. All draft snapshots and revisions change in the
// same transaction. Existing monthly numbers and issued documents stay fixed.
func ReformatCustomerNumber(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		jsonError(w, "Ungültiger Kunde", http.StatusBadRequest)
		return
	}
	var body struct {
		Expected string `json:"expected_customer_no"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || !legacyCustomerNumber.MatchString(body.Expected) {
		jsonError(w, "Bisherige Kundennummer erforderlich", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "Änderung fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var current sql.NullString
	if err := tx.QueryRow(`SELECT customer_no FROM customers WHERE id=?`, id).Scan(&current); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "Kunde nicht gefunden", http.StatusNotFound)
		} else {
			jsonError(w, "Änderung fehlgeschlagen", http.StatusInternalServerError)
		}
		return
	}
	if !current.Valid || current.String != body.Expected {
		jsonError(w, "Kundennummer wurde bereits geändert; bitte neu laden", http.StatusConflict)
		return
	}
	var finalized bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM offers WHERE customer_id=? AND status!='draft')`, id).Scan(&finalized); err != nil {
		jsonError(w, "Änderung fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	if finalized {
		jsonError(w, "Kundennummer ist durch ein finalisiertes Angebot festgelegt", http.StatusConflict)
		return
	}
	number, err := nextCustomerNumber(tx, time.Now())
	if err != nil {
		jsonError(w, "Nummernvergabe fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`UPDATE customers SET customer_no=?,updated_at=datetime('now') WHERE id=?`, number, id); err != nil {
		jsonError(w, "Änderung fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`UPDATE offers SET document=json_set(document,'$.customer.customer_no',?),revision=revision+1,updated_at=datetime('now') WHERE customer_id=? AND status='draft'`, number, id); err != nil {
		jsonError(w, "Änderung fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "Änderung fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"customer_no": number})
}

// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

type pullChange struct {
	Number int64 `json:"number"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

// ConfigureWebhook is startup-only host configuration, never tenant input.
func (m *Module) ConfigureWebhook(secret []byte) { m.webhookSecret = append([]byte(nil), secret...) }

func (m *Module) pullChanged(w http.ResponseWriter, r *http.Request) {
	app, ok := m.publisher.(*GitHubApp)
	if !ok || len(m.webhookSecret) < 32 || !app.Configured(app.Config.TenantID, app.Config.Repository) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	signature := r.Header.Get("X-Hub-Signature-256")
	want, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	mac := hmac.New(sha256.New, m.webhookSecret)
	_, _ = mac.Write(raw)
	if err != nil || len(signature) != 71 || !strings.HasPrefix(signature, "sha256=") || !hmac.Equal(want, mac.Sum(nil)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var event struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Pull pullChange `json:"pull_request"`
	}
	if json.Unmarshal(raw, &event) != nil || r.Header.Get("X-GitHub-Event") != "pull_request" ||
		(event.Action != "edited" && event.Action != "synchronize" && event.Action != "reopened") ||
		event.Pull.Number < 1 || !reviewgate.ValidSHA(event.Pull.Head.SHA) || !reviewgate.ValidSHA(event.Pull.Base.SHA) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if strconv.FormatInt(event.Installation.ID, 10) != app.Config.InstallationID || event.Repository.FullName != app.Config.Repository || event.Pull.Base.Repo.FullName != app.Config.Repository {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	// One latest status owner for the signalled head. This path shares only
	// that head's publication lock, never the dirty reporter's global lock.
	var v Review
	err = db.InTenant(db.AllProjects(ctx, "authenticated review binding change"), m.pool, app.Config.TenantID, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT v.work_order_id::text FROM work_order_reviews v
		 WHERE v.repository=$1 AND v.head_sha=$2 AND v.pull_request=$3 AND v.github_status<>'unconfigured' AND `+latestStatusOwner,
			app.Config.Repository, event.Pull.Head.SHA, event.Pull.Number).Scan(&id)
		if err != nil {
			return err
		}
		v, err = load(ctx, tx, id)
		return err
	})
	if err == pgx.ErrNoRows {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err == nil {
		conn, e := m.pool.Acquire(ctx)
		if e != nil {
			err = e
		} else {
			err = m.publishReview(ctx, conn, app.Config.TenantID, v, &event.Pull)
			conn.Release()
		}
	}
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

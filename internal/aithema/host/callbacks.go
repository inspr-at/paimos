// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

type callback struct {
	ID              string          `json:"id"`
	Session         string          `json:"sid,omitempty"`
	Operation       string          `json:"operation"`
	State           string          `json:"state"`
	Attempts        int             `json:"attempts"`
	Acknowledgement json.RawMessage `json:"acknowledgement,omitempty"`
	payload         json.RawMessage
}

func (m *Module) callbackStatus(w http.ResponseWriter, r *http.Request) {
	p, err := m.person(r, "plugins.manage", "")
	if err != nil {
		writeError(w, err)
		return
	}
	if !uuidRE.MatchString(r.PathValue("callbackId")) {
		writeError(w, fail(404, "not_found"))
		return
	}
	var out callback
	err = db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT id::text,coalesce(sid::text,''),operation,state,attempts,acknowledgement FROM aithema_callbacks WHERE tenant_id=$1 AND id=$2`, p.TenantID, r.PathValue("callbackId")).Scan(&out.ID, &out.Session, &out.Operation, &out.State, &out.Attempts, &out.Acknowledgement)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = fail(404, "not_found")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, out)
}

// Run is a bounded, restart-safe outbox worker. It performs at most one
// callback per tenant per pass. Replica leases and an immutable idempotency
// header permit at-least-once delivery without repeating service effects.
func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	passes := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		rows, err := m.Pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			continue
		}
		var tids []string
		for rows.Next() {
			var tid string
			if rows.Scan(&tid) == nil {
				tids = append(tids, tid)
			}
		}
		rows.Close()
		for _, tid := range tids {
			if ctx.Err() != nil {
				return
			}
			work, cancel := context.WithTimeout(db.AllProjects(ctx, "aithema callback worker"), 15*time.Second)
			if passes%10 == 0 {
				_ = m.reconcile(work, tid)
			}
			_ = m.queueHostEvents(work, tid)
			_, _ = m.DeliverOne(work, tid)
			cancel()
		}
		passes++
	}
}
func (m *Module) DeliverOne(ctx context.Context, tid string) (bool, error) {
	var c callback
	err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
		// A final-attempt crash must not leave a pending row forever.
		if _, err := tx.Exec(ctx, `UPDATE aithema_callbacks SET state='failed',lease_until=NULL WHERE tenant_id=$1 AND state='pending' AND attempts=6 AND (lease_until IS NULL OR lease_until<=now())`, tid); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE aithema_callbacks SET attempts=attempts+1,lease_until=now()+interval '30 seconds' WHERE tenant_id=$1 AND id=(SELECT c.id FROM aithema_callbacks c WHERE c.tenant_id=$1 AND c.state='pending' AND c.attempts<6 AND c.next_attempt_at<=now() AND (c.lease_until IS NULL OR c.lease_until<=now()) AND NOT EXISTS(SELECT 1 FROM aithema_callbacks earlier WHERE earlier.tenant_id=c.tenant_id AND earlier.state='pending' AND earlier.sid IS NOT DISTINCT FROM c.sid AND (earlier.created_at,earlier.id)<(c.created_at,c.id)) ORDER BY c.created_at,c.id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id::text,coalesce(sid::text,''),operation,payload,attempts`, tid).Scan(&c.ID, &c.Session, &c.Operation, &c.payload, &c.Attempts)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	status, ack := m.sendCallback(ctx, tid, c)
	success := status >= 200 && status < 300
	retry := status == 0 || status == 408 || status == 429 || status >= 500
	state := "pending"
	if success {
		state = "delivered"
	} else if !retry || c.Attempts >= 6 {
		state = "failed"
	}
	delay := time.Duration(1<<uint(c.Attempts-1)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	err = db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE aithema_callbacks SET state=$3,acknowledgement=$4,next_attempt_at=$5,lease_until=NULL WHERE tenant_id=$1 AND id=$2 AND attempts=$6`, tid, c.ID, state, ack, m.clock().Add(delay), c.Attempts)
		return err
	})
	return true, err
}
func (m *Module) sendCallback(ctx context.Context, tid string, c callback) (int, json.RawMessage) {
	s, credential, err := m.settings(ctx, tid)
	if err != nil || credential == "" {
		return 0, nil
	}
	path := "/v1/sessions/" + url.PathEscape(c.Session) + "/" + c.Operation
	if c.Operation == "create" {
		path = "/v1/sessions"
	}
	if c.Operation == "deprovision" {
		path = "/v1/deprovision"
	}
	request, err := http.NewRequestWithContext(ctx, "POST", s.ServiceURL+path, bytes.NewReader(c.payload))
	if err != nil {
		return 0, nil
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(credential))
	request.Header.Set("Idempotency-Key", c.ID)
	if c.Operation == "create" || c.Operation == "resume" {
		var project string
		if err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT project_id FROM aithema_sessions WHERE tenant_id=$1 AND sid=$2`, tid, c.Session).Scan(&project)
		}); err != nil {
			return 0, nil
		}
		st, err := m.Journal.Current(ctx, tid, project, c.Session)
		if err != nil {
			return 0, nil
		}
		if st.Tombstone || st.Suspended {
			return 409, nil
		}
		if err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error { _, _, err := m.live(ctx, tx, tid, st, "intake.write"); return err }); err != nil {
			return 409, nil
		}
		pair, err := m.tokenPair(ctx, tid, st)
		if err != nil {
			return 0, nil
		}
		request.Header.Set("X-Aithema-Session-Token", pair.SessionToken)
		request.Header.Set("X-Aithema-Delegated-Token", pair.DelegatedToken)
	}
	resp, err := serviceClient(s).Do(request)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	if c.Operation != "purge" || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || bytes.Contains(raw, []byte(string(credential))) {
		return 502, nil
	}
	var ack struct {
		Session   string   `json:"sid"`
		Purged    bool     `json:"purged"`
		Artifacts []string `json:"host_artifacts"`
	}
	if _, err := tokens.CanonicalJSON(raw); err != nil {
		return 502, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&ack) != nil || ack.Session != c.Session || !ack.Purged || ack.Artifacts == nil || len(ack.Artifacts) > 256 {
		return 502, nil
	}
	for _, artifact := range ack.Artifacts {
		if len(artifact) > 200 || !strings.HasPrefix(artifact, c.Session+":") || strings.ContainsAny(artifact, "\r\n\x00") {
			return 502, nil
		}
	}
	// An acknowledgement lists host artefacts, never commands or paths. It is
	// exposed for host-owned cleanup; the service cannot delete host files.
	safe, _ := json.Marshal(ack)
	return resp.StatusCode, safe
}

// reconcile accelerates offboarding/plugin-removal. Request-time enforcement
// remains authoritative even before this periodic callback is delivered.
func (m *Module) reconcile(ctx context.Context, tid string) error {
	var sessions []struct{ SID, Project string }
	if err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT h.sid::text,s.project_id FROM aithema_host_sessions h JOIN aithema_sessions s USING(tenant_id,sid) WHERE h.tenant_id=$1 AND NOT s.tombstone ORDER BY h.sid`, tid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st struct{ SID, Project string }
			if err := rows.Scan(&st.SID, &st.Project); err != nil {
				return err
			}
			sessions = append(sessions, st)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, st := range sessions {
		var denied bool
		if err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
			state, err := m.Journal.LockAuthority(ctx, tx, tid, st.SID)
			if err != nil {
				return err
			}
			_, _, err = m.live(ctx, tx, tid, state, "intake.write")
			var f *Fault
			denied = errors.Is(err, authz.ErrForbidden) || errors.As(err, &f) && f.Code == "revoked"
			if err != nil && !denied {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		if !denied {
			continue
		}
		if err := db.InTransaction(ctx, m.Pool, func(ctx context.Context) error {
			if err := m.Journal.Revoke(ctx, tid, st.Project, st.SID, "purge"); err != nil {
				return err
			}
			return db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
				_, err := enqueue(ctx, tx, tid, st.SID, "revoke", "offboard:"+st.SID, map[string]string{"sid": st.SID, "action": "purge"})
				return err
			})
		}); err != nil {
			return err
		}
	}
	return nil
}

// SPDX-License-Identifier: AGPL-3.0-only
package phoneapprovals

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

type Preferences struct {
	Enabled    bool   `json:"enabled"`
	Zone       string `json:"time_zone"`
	Start      int    `json:"quiet_start"`
	End        int    `json:"quiet_end"`
	Escalation int    `json:"escalation_minutes"`
}
type item struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created_at"`
}

func preference(ctx context.Context, tx pgx.Tx, person string) (Preferences, error) {
	p := Preferences{Zone: "UTC", Escalation: 15}
	err := tx.QueryRow(ctx, `SELECT enabled,time_zone,quiet_start,quiet_end,escalation_minutes FROM phone_approval_preferences WHERE person_id=$1`, person).Scan(&p.Enabled, &p.Zone, &p.Start, &p.End, &p.Escalation)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (m *Module) settings(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, false)
	if !ok {
		return
	}
	keys, subs := []item{}, []item{}
	var prefs Preferences
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		prefs, err = preference(r.Context(), tx, p.ID)
		if err != nil {
			return err
		}
		for _, entry := range []struct {
			query string
			dst   *[]item
		}{{`SELECT id::text,created_at FROM phone_passkeys WHERE person_id=$1 AND revoked_at IS NULL ORDER BY created_at`, &keys}, {`SELECT id::text,created_at FROM phone_push_subscriptions WHERE person_id=$1 AND revoked_at IS NULL ORDER BY created_at`, &subs}} {
			rows, err := tx.Query(r.Context(), entry.query, p.ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var it item
				if err = rows.Scan(&it.ID, &it.Created); err != nil {
					rows.Close()
					return err
				}
				*entry.dst = append(*entry.dst, it)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	pub := ""
	if m.vapid != nil {
		pub = m.vapid.VAPIDPublicKey
	}
	respond(w, map[string]any{"available": m.wa != nil, "push_available": m.vapid != nil, "vapid_public_key": pub, "preferences": prefs, "passkeys": keys, "subscriptions": subs}, err)
}
func validatePreferences(p Preferences) error {
	_, err := time.LoadLocation(p.Zone)
	if err != nil || len(p.Zone) > 100 || p.Start < 0 || p.Start > 1439 || p.End < 0 || p.End > 1439 || p.Escalation < 5 || p.Escalation > 1440 {
		return fail(400, "invalid quiet hours or escalation interval")
	}
	return nil
}
func (m *Module) preferences(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	var in Preferences
	err := decode(w, r, &in)
	if err == nil {
		err = validatePreferences(in)
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if in.Enabled {
			u, err := loadUser(r.Context(), tx, p)
			if err != nil {
				return err
			}
			if len(u.creds) == 0 {
				return fail(409, "add a passkey before enabling phone notifications")
			}
			if m.vapid == nil {
				return fail(503, "push is not configured")
			}
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO phone_approval_preferences(tenant_id,person_id,enabled,time_zone,quiet_start,quiet_end,escalation_minutes) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,person_id) DO UPDATE SET enabled=excluded.enabled,time_zone=excluded.time_zone,quiet_start=excluded.quiet_start,quiet_end=excluded.quiet_end,escalation_minutes=excluded.escalation_minutes`, p.TenantID, p.ID, in.Enabled, in.Zone, in.Start, in.End, in.Escalation)
		if err != nil {
			return err
		}
		return audit(r.Context(), tx, p, "phone_approval.preferences_changed", in)
	})
	respond(w, in, err)
}
func (m *Module) registerOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	var out any
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		u, err := loadUser(r.Context(), tx, p)
		if err != nil {
			return err
		}
		if len(u.creds) >= 8 {
			return fail(409, "revoke an old passkey before adding another")
		}
		options, session, err := m.wa.BeginRegistration(u)
		if err != nil {
			return fail(403, "passkey registration unavailable")
		}
		id, err := storeSession(r.Context(), tx, p, "registration", "", "", session)
		out = map[string]any{"challenge_id": id, "publicKey": options.Response}
		return err
	})
	respond(w, out, err)
}
func (m *Module) register(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	var proof Proof
	if err := decode(w, r, &proof); err != nil {
		respond(w, nil, err)
		return
	}
	var out item
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		u, err := loadUser(r.Context(), tx, p)
		if err != nil {
			return err
		}
		if len(u.creds) >= 8 {
			return fail(409, "passkey limit reached")
		}
		session, err := consume(r.Context(), tx, p, proof.ChallengeID, "registration", "", "")
		if err != nil {
			return err
		}
		parsed, err := protocol.ParseCredentialCreationResponseBytes(proof.Credential)
		if err != nil {
			return fail(403, "registration rejected")
		}
		c, err := m.wa.CreateCredential(u, session, parsed)
		if err != nil || c == nil {
			return fail(403, "registration requires user verification")
		}
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO phone_passkeys(tenant_id,person_id,credential_id,credential) VALUES($1,$2,$3,$4) RETURNING id::text,created_at`, p.TenantID, p.ID, base64.RawURLEncoding.EncodeToString(c.ID), b).Scan(&out.ID, &out.Created)
		if err != nil {
			return fail(409, "passkey already registered")
		}
		return audit(r.Context(), tx, p, "phone_approval.passkey_registered", map[string]string{"passkey_id": out.ID})
	})
	respond(w, out, err)
}
func (m *Module) revokePasskey(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("credentialId")
	if !validID(id) {
		respond(w, nil, fail(404, "passkey unavailable"))
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Lock the same credential rows as assertion verification, so revocation and
		// decisions serialize even when the device tries an outstanding challenge.
		if _, err := loadUser(r.Context(), tx, p); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE phone_passkeys SET revoked_at=coalesce(revoked_at,now()) WHERE id=$1 AND person_id=$2`, id, p.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(404, "passkey unavailable")
		}
		if _, err = tx.Exec(r.Context(), `UPDATE phone_approval_challenges SET consumed_at=now() WHERE person_id=$1 AND consumed_at IS NULL`, p.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE phone_approval_preferences SET enabled=false WHERE person_id=$1 AND NOT EXISTS(SELECT 1 FROM phone_passkeys WHERE person_id=$1 AND revoked_at IS NULL)`, p.ID); err != nil {
			return err
		}
		return audit(r.Context(), tx, p, "phone_approval.passkey_revoked", map[string]string{"passkey_id": id})
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	w.WriteHeader(204)
}
func validSubscription(s webpush.Subscription) bool {
	u, err := url.Parse(s.Endpoint)
	if err != nil || len(s.Endpoint) > 2048 || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Path == "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com":
	default:
		return false
	}
	pub, e1 := base64.RawURLEncoding.DecodeString(s.Keys.P256dh)
	auth, e2 := base64.RawURLEncoding.DecodeString(s.Keys.Auth)
	if e1 != nil || e2 != nil || len(auth) != 16 {
		return false
	}
	_, err = ecdh.P256().NewPublicKey(pub)
	return err == nil
}
func (m *Module) subscribe(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	if m.vapid == nil {
		respond(w, nil, fail(503, "push is not configured"))
		return
	}
	var in webpush.Subscription
	if err := decode(w, r, &in); err != nil || !validSubscription(in) {
		respond(w, nil, fail(400, "invalid or unsupported push subscription"))
		return
	}
	b, err := json.Marshal(in)
	if err != nil {
		respond(w, nil, err)
		return
	}
	encrypted, err := seal(m.vault, b, subscriptionAAD(p.TenantID, p.ID))
	if err != nil {
		respond(w, nil, err)
		return
	}
	var out item
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		u, err := loadUser(r.Context(), tx, p)
		if err != nil {
			return err
		}
		if len(u.creds) == 0 {
			return fail(409, "add a passkey first")
		}
		var n int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM phone_push_subscriptions WHERE person_id=$1 AND revoked_at IS NULL AND endpoint_hash<>$2`, p.ID, digest(in.Endpoint)).Scan(&n); err != nil {
			return err
		}
		if n >= 8 {
			return fail(409, "subscription limit reached")
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO phone_push_subscriptions(tenant_id,person_id,endpoint_hash,subscription) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,endpoint_hash) DO UPDATE SET subscription=excluded.subscription,revoked_at=NULL WHERE phone_push_subscriptions.person_id=excluded.person_id RETURNING id::text,created_at`, p.TenantID, p.ID, digest(in.Endpoint), encrypted).Scan(&out.ID, &out.Created)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(409, "subscription belongs to another account; disable it there first")
		}
		if err != nil {
			return err
		}
		return audit(r.Context(), tx, p, "phone_approval.subscribed", map[string]string{"subscription_id": out.ID})
	})
	respond(w, out, err)
}
func (m *Module) unsubscribe(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("subscriptionId")
	if !validID(id) {
		respond(w, nil, fail(404, "subscription unavailable"))
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE phone_push_subscriptions SET revoked_at=coalesce(revoked_at,now()) WHERE id=$1 AND person_id=$2`, id, p.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(404, "subscription unavailable")
		}
		return audit(r.Context(), tx, p, "phone_approval.unsubscribed", map[string]string{"subscription_id": id})
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	w.WriteHeader(204)
}
func quiet(p Preferences, now time.Time) bool {
	if p.Start == p.End {
		return false
	}
	loc, err := time.LoadLocation(p.Zone)
	if err != nil {
		return true
	}
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	if p.Start < p.End {
		return minute >= p.Start && minute < p.End
	}
	return minute >= p.Start || minute < p.End
}

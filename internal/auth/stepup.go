// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/stepup"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type stepupLogin struct {
	PersonID string             `json:"person_id"`
	Start    stepup.ReauthStart `json:"start"`
}

// BeginStepUp reuses the deployment's OIDC client, signing key and callback.
// The signed cookie carries the exact request/challenge/person binding; the
// persisted challenge makes the verified callback one-use across replicas.
func (m *Module) BeginStepUp(w http.ResponseWriter, r *http.Request, p tenant.Principal, start stepup.ReauthStart) (string, error) {
	if p.Kind != tenant.Person || !p.BrowserSession || p.KeyCreatorID != "" {
		return "", errors.New("browser person required")
	}
	_, oc, err := m.oidcProvider(r.Context())
	if err != nil {
		return "", err
	}
	state, err := randomString(16)
	if err != nil {
		return "", err
	}
	nonce, err := randomString(16)
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	payload := oidcPayload{State: state, Nonce: nonce, Verifier: verifier, Tenant: p.TenantID, Exp: start.StartedAt.Add(stepup.ProofLifetime).Unix(), StepUp: &stepupLogin{p.ID, start}}
	if err := m.setOIDCCookie(w, payload); err != nil {
		return "", err
	}
	return oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0")), nil
}
func (m *Module) finishStepUp(w http.ResponseWriter, r *http.Request, payload oidcPayload, idt *oidc.IDToken) {
	fail := func(reason string) { m.callbackFailure(w, r, "failed", reason) }
	login := payload.StepUp
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || !p.BrowserSession || p.KeyCreatorID != "" || p.ID != login.PersonID || p.TenantID != payload.Tenant || m.StepUp == nil {
		fail("stepup_person_mismatch")
		return
	}
	var matches bool
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM principals p JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1 AND p.id=$2 AND p.kind='person' AND i.issuer=$3 AND i.subject=$4)`, p.TenantID, p.ID, idt.Issuer, idt.Subject).Scan(&matches)
	})
	if err != nil || !matches {
		fail("stepup_identity_mismatch")
		return
	}
	var claims struct {
		AuthTime int64 `json:"auth_time"`
	}
	if idt.Claims(&claims) != nil || claims.AuthTime <= login.Start.StartedAt.Unix() {
		fail("stepup_auth_time")
		return
	}
	out, err := m.StepUp.FinishReauth(r.Context(), p, login.Start, time.Unix(claims.AuthTime, 0).UTC())
	if err != nil {
		fail("stepup_verification")
		return
	}
	m.clearOIDCCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/decision-desk?needs=s:"+out.ID, http.StatusFound)
}

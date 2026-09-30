// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type watchSecurityWrite struct {
	ConsentMode string `json:"consent_mode"`
}

type localAuthComputer struct {
	PairingUpgraded bool   `json:"pairing_upgraded"`
	ComputerID      string `json:"computer_id"`
	Name            string `json:"name"`
	Capability      string `json:"capability"`
}

type watchSecurityView struct {
	ConsentMode        string              `json:"consent_mode"`
	ConsentSaved       bool                `json:"consent_saved"`
	LocalAuthComputers []localAuthComputer `json:"local_auth_computers"`
}

// A missing row is the unsaved default, not an explicit Aeon opt-out.
func watchConsentPreference(ctx context.Context, tx pgx.Tx, owner string) (mode string, saved bool, err error) {
	err = tx.QueryRow(ctx, `SELECT consent_mode FROM person_watch_security WHERE person_id=$1`, owner).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachwatch.ConsentAeon, false, nil
	}
	return mode, true, err
}

// pairingLocalAuth returns the platform stored when this computer was paired
// and whether browser approval pinned a local confirmation public key.
func pairingLocalAuth(ctx context.Context, tx pgx.Tx, computer string) (platform string, upgraded bool, err error) {
	err = tx.QueryRow(ctx, `SELECT coalesce(q.details->>'platform',''), c.local_auth_public_key<>''
 FROM agent_pairing_computers c
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE c.id=$1 AND c.state='connected'`, computer).Scan(&platform, &upgraded)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return platform, upgraded, err
}

// Once a pairing has pinned a public key, the signature is required no matter
// what capability the daemon reports. The snapshot platform must match the
// platform stored at pairing; a lied platform cannot select Aeon approval.
// Pairings without a key stay on Aeon approval. A saved aeon choice is an
// explicit opt-out, and only for a snapshot that matches the paired platform.
func computerWatchConsentMode(ctx context.Context, tx pgx.Tx, owner, snapshotPlatform, computer string) (string, error) {
	mode, saved, err := watchConsentPreference(ctx, tx, owner)
	if err != nil {
		return "", err
	}
	pairingPlatform, upgraded, err := pairingLocalAuth(ctx, tx, computer)
	if err != nil {
		return "", err
	}
	if upgraded && snapshotPlatform != pairingPlatform {
		return "", fail(409, "conflict", "snapshot platform does not match the paired computer")
	}
	if !upgraded {
		if !saved || mode == attachwatch.ConsentLocalAuth {
			return attachwatch.ConsentAeon, nil
		}
		return mode, nil
	}
	if saved && mode == attachwatch.ConsentAeon {
		return attachwatch.ConsentAeon, nil
	}
	return attachwatch.ConsentLocalAuth, nil
}

func localAuthComputers(ctx context.Context, tx pgx.Tx, person string) ([]localAuthComputer, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text, coalesce(q.details->>'computer_name', ''), c.local_auth_capability, c.local_auth_public_key<>''
 FROM agent_pairing_computers c
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE c.state='connected' AND q.approved_by=$1
 ORDER BY q.details->>'computer_name', c.id`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []localAuthComputer{}
	for rows.Next() {
		var item localAuthComputer
		if err = rows.Scan(&item.ComputerID, &item.Name, &item.Capability, &item.PairingUpgraded); err != nil {
			return nil, err
		}
		if item.Name == "" {
			item.Name = "Paired computer"
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (m *Module) watchSecurity(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var write watchSecurityWrite
	if r.Method == "PUT" && (decode(w, r, &write) != nil || !attachwatch.ConsentModeValid(write.ConsentMode)) {
		WriteError(w, fail(400, "invalid_request", "choose aeon or local_auth consent"))
		return
	}
	var out watchSecurityView
	// Same transaction lock as approval: a setting change cannot race the pin.
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if r.Method == "PUT" {
			if _, err := tx.Exec(r.Context(), `INSERT INTO person_watch_security(tenant_id,person_id,consent_mode) VALUES($1,$2,$3) ON CONFLICT(tenant_id,person_id) DO UPDATE SET consent_mode=excluded.consent_mode`, p.TenantID, p.ID, write.ConsentMode); err != nil {
				return err
			}
		}
		mode, saved, err := watchConsentPreference(r.Context(), tx, p.ID)
		if err != nil {
			return err
		}
		out.ConsentSaved = saved
		out.ConsentMode = mode
		out.LocalAuthComputers, err = localAuthComputers(r.Context(), tx, p.ID)
		if err != nil || saved {
			return err
		}
		for _, computer := range out.LocalAuthComputers {
			if computer.PairingUpgraded {
				out.ConsentMode = attachwatch.ConsentLocalAuth
				break
			}
		}
		return nil
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	if out.LocalAuthComputers == nil {
		out.LocalAuthComputers = []localAuthComputer{}
	}
	reply(w, out)
}

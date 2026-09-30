// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
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
	LocalAuthComputers []localAuthComputer `json:"local_auth_computers"`
}

func watchConsentMode(ctx context.Context, tx pgx.Tx, owner string) (string, error) {
	var mode string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT consent_mode FROM person_watch_security WHERE person_id=$1),'aeon')`, owner).Scan(&mode)
	return mode, err
}

// Old pairings cannot prove local confirmation. Their effective policy is Aeon
// approval until a fresh, browser-reviewed pairing pins the public key.
func computerWatchConsentMode(ctx context.Context, tx pgx.Tx, owner, computer string) (string, error) {
	mode, err := watchConsentMode(ctx, tx, owner)
	if err != nil || mode != attachwatch.ConsentLocalAuth {
		return mode, err
	}
	var upgraded bool
	err = tx.QueryRow(ctx, `SELECT local_auth_public_key<>'' FROM agent_pairing_computers WHERE id=$1`, computer).Scan(&upgraded)
	if !upgraded {
		mode = attachwatch.ConsentAeon
	}
	return mode, err
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
		var err error
		out.ConsentMode, err = watchConsentMode(r.Context(), tx, p.ID)
		if err != nil {
			return err
		}
		out.LocalAuthComputers, err = localAuthComputers(r.Context(), tx, p.ID)
		return err
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

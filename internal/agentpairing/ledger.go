// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const LedgerCapability = "ledger-v1"
const LedgerGenerationHeader = "X-Aeon-Ledger-Generation"

// LedgerMode is tenant-wide, including computers which have never enrolled.
func LedgerMode(ctx context.Context, tx pgx.Tx) (bool, error) {
	var mode bool
	err := tx.QueryRow(ctx, `SELECT ledger_mode_at IS NOT NULL FROM account_use_rules`).Scan(&mode)
	return mode, err
}

// LedgerWorkAllowed runs under the pairing fence shared with enrolment and
// approval. No computer row is a denial once the tenant enters ledger mode.
// The generation is a protocol coordinate, never a credential or release claim.
func LedgerWorkAllowed(ctx context.Context, tx pgx.Tx, p tenant.Principal, generation string) (bool, error) {
	mode, err := LedgerMode(ctx, tx)
	if err != nil || !mode {
		return !mode && err == nil, err
	}
	// Bound client input before sending it to SQL, including oversized headers.
	if len(generation) != 36 || !uuidRE.MatchString(generation) {
		generation = ""
	}
	var allowed bool
	var protocol, release, scheme string
	err = tx.QueryRow(ctx, `SELECT c.state='connected' AND q.state='redeemed'
 AND c.ledger_generation IS NOT NULL AND c.ledger_generation=$2 AND $2<>'',
 c.agent_protocol,c.agent_version,c.agent_version_scheme
 FROM agent_pairing_computers c JOIN agent_pairing_requests q
 ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.principal_id=$1`, p.ID, generation).
		Scan(&allowed, &protocol, &release, &scheme)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err == nil && !allowed {
		slog.WarnContext(ctx, "daemon ledger enrolment required", "tenant_id", p.TenantID,
			"principal_id", p.ID, "agent_protocol", protocol, "agent_version", release, "agent_version_scheme", scheme)
	}
	return allowed, err
}

func RequireLedgerWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, generation string) error {
	allowed, err := LedgerWorkAllowed(ctx, tx, p, generation)
	if err != nil {
		return err
	}
	if !allowed {
		return fail(409, "ledger_enrollment_required", "ledger enrolment and matching generation required")
	}
	return nil
}

func (m *Module) enrollLedger(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Agent {
		WriteError(w, fail(403, "forbidden", "paired runtime required"))
		return
	}
	var in struct {
		Generation string `json:"generation"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if !uuidRE.MatchString(in.Generation) {
		WriteError(w, fail(400, "invalid_request", "random 128-bit generation UUID required"))
		return
	}
	var out View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := accountuse.LockExclusive(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "run.claim", authz.Scope{}); err != nil {
			return fail(403, "forbidden", "runtime permission required")
		}
		var id, state, requestState string
		var before *string
		if err := tx.QueryRow(ctx, `SELECT c.id::text,c.state,q.state,c.ledger_generation
 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 WHERE c.principal_id=$1 FOR NO KEY UPDATE OF c`, p.ID).Scan(&id, &state, &requestState, &before); err != nil {
			return notFound(err)
		}
		if state != "connected" || requestState != "redeemed" {
			return fail(409, "ledger_enrollment_required", "connected redeemed computer required")
		}
		if _, err := tx.Exec(ctx, `UPDATE account_use_rules SET ledger_mode_at=coalesce(ledger_mode_at,clock_timestamp())`); err != nil {
			return err
		}
		if err := accountuse.ActivateForLedger(ctx, tx); err != nil {
			return err
		}
		changed := before == nil || *before != in.Generation
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET ledger_generation=$2,ledger_enrolled_at=clock_timestamp(),revision=revision+1 WHERE id=$1`, id, in.Generation); err != nil {
				return err
			}
		}
		rec, err := computerRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = view(ctx, tx, rec, false)
		if err != nil || !changed {
			return err
		}
		// All resources and the persisted response precede the event counter.
		return audit(ctx, tx, p, "agent_pairing.ledger_enrolled", map[string]any{
			"computer_id": id, "generation": out.LedgerGeneration, "ledger_mode": out.LedgerMode,
		})
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

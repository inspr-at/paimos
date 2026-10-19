// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/statusnames"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// StatusNamesEvent tells open views that the workspace's status names changed.
const StatusNamesEvent = "tenant.status_names_updated"

// statusNamesJSON is the workspace's own words for its statuses (AEON-1401).
// Revision 0 means nothing was ever saved.
type statusNamesJSON struct {
	Revision int64             `json:"revision"`
	Names    statusnames.Names `json:"names"`
}

// LoadStatusNames reads the workspace's status names in the caller's tenant
// transaction. No row reads as no renames.
func LoadStatusNames(ctx context.Context, tx pgx.Tx) (int64, statusnames.Names, error) {
	var revision int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,names FROM status_names WHERE tenant_id=current_setting('aeon.tenant_id')::uuid`).Scan(&revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, statusnames.Names{}, nil
	}
	if err != nil {
		return 0, nil, err
	}
	names := statusnames.Names{}
	if err := json.Unmarshal(raw, &names); err != nil {
		return 0, nil, err
	}
	return revision, names, nil
}

func (m *Module) handleGetStatusNames(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	var out statusNamesJSON
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out.Revision, out.Names, err = LoadStatusNames(ctx, tx)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handlePutStatusNames(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, http.StatusForbidden, "person required")
		return
	}
	// Twelve statuses with two 24-character names each fit well inside 8 KiB.
	r.Body = httpapi.RequestBodyReader(w, r, 8192)
	var input struct {
		Revision *int64            `json:"revision"`
		Names    statusnames.Names `json:"names"`
	}
	// Unknown fields are refused at every level, including inside a name.
	dec := httpapi.NewRequestDecoder(w, r, 8192)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid status names")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid status names")
		return
	}
	if input.Revision == nil || *input.Revision < 0 || input.Names == nil || len(input.Names) > len(statusnames.Keys) {
		writeError(w, http.StatusBadRequest, "revision and names are required")
		return
	}
	names, err := statusnames.Normalize(input.Names)
	if err != nil {
		var refused *statusnames.Error
		if errors.As(err, &refused) {
			writeErr(w, &httpError{status: http.StatusBadRequest, msg: refused.Msg, code: refused.Code, fields: []string{refused.Field()}})
			return
		}
		writeErr(w, err)
		return
	}
	out := statusNamesJSON{Names: names}
	err = m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		// The tenant fence also serialises access changes; no lock follows the event.
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return &httpError{status: http.StatusForbidden, msg: "permission denied"}
		}
		revision, prior, err := LoadStatusNames(ctx, tx)
		if err != nil {
			return err
		}
		if revision != *input.Revision {
			return conflict("status names changed; reload before saving")
		}
		out.Revision = revision + 1
		raw, err := json.Marshal(names)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO status_names(tenant_id,revision,names) VALUES($1,$2,$3)
			ON CONFLICT(tenant_id) DO UPDATE SET revision=EXCLUDED.revision,names=EXCLUDED.names,updated_at=clock_timestamp()`, p.TenantID, out.Revision, raw); err != nil {
			return err
		}
		before, err := json.Marshal(statusNamesJSON{Revision: revision, Names: prior})
		if err != nil {
			return err
		}
		after, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return m.events.WriteEvent(ctx, tx, Event{ActorPrincipalID: p.ID, Type: StatusNamesEvent, Before: before, After: after})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

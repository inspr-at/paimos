// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type releaseInput struct {
	Key string   `json:"idempotency_key"`
	IDs []string `json:"ticket_node_ids"`
}

func (m *module) createRelease(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := projectPrincipal(w, r, true)
	if !ok {
		return
	}
	var in releaseInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(in.Key) == "" || len(in.Key) > 96 || len(in.IDs) < 1 || len(in.IDs) > 100 {
		respond(w, nil, fail(400, "idempotency_key and 1 to 100 ticket_node_ids are required"))
		return
	}
	for i, id := range in.IDs {
		in.IDs[i] = strings.ToLower(id)
		if !uuid.MatchString(in.IDs[i]) {
			respond(w, nil, fail(400, "ticket_node_ids must be unique UUIDs"))
			return
		}
	}
	sort.Strings(in.IDs)
	if len(slices.Compact(slices.Clone(in.IDs))) != len(in.IDs) {
		respond(w, nil, fail(400, "ticket_node_ids must be unique UUIDs"))
		return
	}
	project := strings.ToLower(r.PathValue("projectId"))
	var out membershipResult
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = openPlanningRelease(ctx, tx, p, project, in)
		return err
	})
	respond(w, out, err)
}

// The legacy-named tables remain the release ledger during the expansion window.
// Access and tree fences precede record locks; membership acquires the event
// counter only after every node and membership write has completed.
func openPlanningRelease(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in releaseInput) (membershipResult, error) {
	var out membershipResult
	if err := lockMembership(ctx, tx, p, project); err != nil {
		return out, err
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project}); err != nil {
		return out, fail(403, "project write access required")
	}
	var allowed []string
	var unrestricted bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(k.allowed_child_kinds,'{}'::text[]),k.allowed_child_kinds IS NULL FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project' FOR UPDATE OF n`, project).Scan(&allowed, &unrestricted); err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var priorDigest string
	var prior json.RawMessage
	err := tx.QueryRow(ctx, `SELECT r.request_sha256,e.after FROM journey_action_receipts r JOIN events e ON e.tenant_id=r.tenant_id AND e.id=r.event_id WHERE r.project_node_id=$1 AND r.idempotency_key=$2`, project, "release:"+in.Key).Scan(&priorDigest, &prior)
	if err == nil {
		if priorDigest != digest {
			return out, fail(409, "idempotency key was used for another release request")
		}
		return out, json.Unmarshal(prior, &out)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if !unrestricted && !slices.Contains(allowed, "release") {
		return out, fail(409, "project cannot contain a release")
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id WHERE r.project_node_id=$1 AND r.state NOT IN ('released','superseded') AND n.deleted_at IS NULL)`, project).Scan(&active); err != nil {
		return out, err
	}
	if active {
		return out, fail(409, "finish the current release before opening another")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, p.TenantID, project); err != nil {
		return out, err
	}
	var number int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number),0)+1 FROM journey_releases WHERE project_node_id=$1`, project).Scan(&number); err != nil {
		return out, err
	}
	var key, id string
	if err := tx.QueryRow(ctx, `SELECT aeon_next_node_key($1,'REL')`, p.TenantID).Scan(&key); err != nil {
		return out, err
	}
	var node json.RawMessage
	if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,position) SELECT $1,$2,k.id,$3,$4,$5 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='release' RETURNING id::text,to_jsonb(nodes)`, p.TenantID, key, fmt.Sprintf("Release %d", number), project, number).Scan(&id, &node); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state) VALUES($1,$2,$3,$4,'planning')`, p.TenantID, id, project, number); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_projects SET current_release_node_id=$2,revision=revision+1,updated_at=clock_timestamp() WHERE project_node_id=$1`, project, id); err != nil {
		return out, err
	}
	out, err = addExisting(ctx, tx, p, project, id, membershipInput{Revision: 1, IDs: in.IDs})
	if err != nil {
		return out, err
	}
	if _, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "node.created", After: node}); err != nil {
		return out, err
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "release.created", After: out})
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO journey_action_receipts(tenant_id,project_node_id,idempotency_key,request_sha256,resulting_revision,event_id) SELECT $1,$2,$3,$4,revision,$5 FROM journey_projects WHERE project_node_id=$2`, p.TenantID, project, "release:"+in.Key, digest, ev.ID)
	return out, err
}

type planningRelease struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Number int    `json:"number"`
	State  string `json:"state"`
}

func (m *module) listPlanningReleases(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := projectPrincipal(w, r, false)
	if !ok {
		return
	}
	project := strings.ToLower(r.PathValue("projectId"))
	before := 0
	if value := r.URL.Query().Get("before_number"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			respond(w, nil, fail(400, "invalid before_number"))
			return
		}
		before = parsed
	}
	out := struct {
		Releases  []planningRelease `json:"releases"`
		Truncated bool              `json:"truncated"`
	}{Releases: []planningRelease{}}
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}) != nil {
			return fail(403, "project access required")
		}
		rows, err := tx.Query(ctx, `SELECT r.release_node_id::text,n.title,r.number,r.state FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id WHERE r.project_node_id=$1 AND n.deleted_at IS NULL AND ($2::int=0 OR r.number<$2::int) ORDER BY r.number DESC LIMIT 101`, project, before)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var release planningRelease
			if err := rows.Scan(&release.ID, &release.Title, &release.Number, &release.State); err != nil {
				return err
			}
			if len(out.Releases) == 100 {
				out.Truncated = true
				break
			}
			out.Releases = append(out.Releases, release)
		}
		return rows.Err()
	})
	respond(w, out, err)
}

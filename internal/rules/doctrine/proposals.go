// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Proposal holds references and workflow evidence, never proposed prose.
type Proposal struct {
	ID               string    `json:"id"`
	SourceID         string    `json:"source_id"`
	Repository       string    `json:"repository"`
	Path             string    `json:"path"`
	RuleKey          string    `json:"rule_key"`
	State            string    `json:"state"`
	HeadSHA          string    `json:"head_sha"`
	PRNumber         int       `json:"pr_number"`
	PRURL            string    `json:"pr_url"`
	ProposedBy       string    `json:"proposed_by"`
	ApprovedBy       string    `json:"approved_by,omitempty"`
	MergeCommit      string    `json:"merge_commit,omitempty"`
	Release          string    `json:"release,omitempty"`
	ReleaseCommit    string    `json:"release_commit,omitempty"`
	ReleaseURL       string    `json:"release_url,omitempty"`
	ReleaseRequested bool      `json:"release_requested,omitempty"`
	GateReady        bool      `json:"gate_ready"`
	GateReason       string    `json:"gate_reason,omitempty"`
	PinnedMachines   int       `json:"pinned_machines"`
	CreatedAt        time.Time `json:"created_at"`
	GateReview       int64     `json:"-"`
	BaseCommit       string    `json:"-"`
	InputDigest      string    `json:"-"`
}

type proposalData struct {
	Proposal
	GateReviewID int64 `json:"gate_review_id,omitempty"`
}

const proposalColumns = `id::text,source_id::text,repository,path,rule_key,input_digest,base_commit,proposed_by::text,created_at,data`

func scanProposal(row pgx.Row) (Proposal, error) {
	var p Proposal
	var raw []byte
	err := row.Scan(&p.ID, &p.SourceID, &p.Repository, &p.Path, &p.RuleKey, &p.InputDigest, &p.BaseCommit, &p.ProposedBy, &p.CreatedAt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fail(404, "not_found", "That proposal is not in this workspace.")
	}
	if err != nil {
		return p, err
	}
	var d proposalData
	if err = json.Unmarshal(raw, &d); err != nil {
		return p, err
	}
	d.ID, d.SourceID, d.Repository, d.Path, d.RuleKey, d.InputDigest, d.BaseCommit, d.ProposedBy, d.CreatedAt = p.ID, p.SourceID, p.Repository, p.Path, p.RuleKey, p.InputDigest, p.BaseCommit, p.ProposedBy, p.CreatedAt
	d.GateReview = d.GateReviewID
	if d.State == "" {
		d.State = "proposed"
	}
	return d.Proposal, nil
}
func getProposal(ctx context.Context, tx pgx.Tx, id string) (Proposal, error) {
	return scanProposal(tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM doctrine_proposals WHERE id=$1 FOR UPDATE`, id))
}
func countPins(ctx context.Context, tx pgx.Tx, p *Proposal) error {
	if p.ReleaseCommit == "" {
		return nil
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM doctrine_machine_pins WHERE repository=$1 AND commit_sha=$2`, p.Repository, p.ReleaseCommit).Scan(&p.PinnedMachines); err != nil {
		return err
	}
	p.State = "released"
	if p.PinnedMachines > 0 {
		p.State = "pinned"
	}
	return nil
}
func saveProposal(ctx context.Context, tx pgx.Tx, actor tenant.Principal, p Proposal, event string) error {
	raw, err := json.Marshal(proposalData{Proposal: p, GateReviewID: p.GateReview})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE doctrine_proposals SET data=$2 WHERE id=$1`, p.ID, raw); err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, actor, events.Change{Type: event, After: map[string]any{
		"proposal_id": p.ID, "repository": p.Repository, "pr_number": p.PRNumber, "head_sha": p.HeadSHA, "state": p.State, "proposed_by": p.ProposedBy, "approved_by": p.ApprovedBy, "gate_review_id": p.GateReview, "merge_commit": p.MergeCommit, "release": p.Release, "release_commit": p.ReleaseCommit, "release_requested": p.ReleaseRequested,
	}})
	return err
}
func (m *Module) proposalAccess(p tenant.Principal) error {
	if !m.app.configured() {
		return fail(503, "app_unavailable", "Doctrine proposals need a server-provisioned GitHub App and independent review gate.")
	}
	if p.TenantID != m.app.TenantID {
		return authz.ErrForbidden
	}
	return nil
}
func (m *Module) listProposals(r *http.Request, actor tenant.Principal) (any, error) {
	out := struct {
		Proposals []Proposal `json:"proposals"`
	}{Proposals: []Proposal{}}
	err := m.tx(r.Context(), actor, "rules.read", func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+proposalColumns+` FROM doctrine_proposals ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return err
		}
		for rows.Next() {
			p, err := scanProposal(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out.Proposals = append(out.Proposals, p)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		for i := range out.Proposals {
			if err = countPins(r.Context(), tx, &out.Proposals[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
func (m *Module) propose(r *http.Request, actor tenant.Principal) (any, error) {
	if err := m.proposalAccess(actor); err != nil {
		return nil, err
	}
	var in ProposalInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	var source Source
	var files []File
	var existing *Proposal
	err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		p, err := getProposal(ctx, tx, in.RequestID)
		if err == nil {
			if p.InputDigest != inputDigest(in) || p.ProposedBy != actor.ID {
				return fail(409, "request_conflict", "That request UUID belongs to another proposal.")
			}
			existing = &p
			if p.PRNumber > 0 {
				return nil
			}
		}
		if err != nil {
			var f *failure
			if !errors.As(err, &f) || f.Status != 404 {
				return err
			}
		}
		source, err = getSource(ctx, tx, in.SourceID, false)
		if err != nil {
			return err
		}
		if !writableSource(source) {
			return fail(422, "unsupported_repository", "Only the public and private INSPR doctrine repositories accept proposals; their visibility must match.")
		}
		if source.CredentialRef != "" {
			if err := m.credentials.authorize(source.CredentialRef, actor.TenantID, source.Repository); err != nil {
				return err
			}
		}
		files, err = cachedFiles(ctx, tx, source)
		return err
	})
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.PRNumber > 0 {
		return *existing, nil
	}
	changed, err := editRule(source, files, in)
	if err != nil {
		return nil, err
	}
	g, err := m.appClient(ctx, actor.TenantID, source.Repository)
	if err != nil {
		return nil, err
	}
	main, err := g.main(ctx, source.Repository)
	if err != nil {
		return nil, err
	}
	// Compare both touched files with main. Never replace newer source/sidecar
	// bytes with a stale pinned view. Unrelated main commits are safe to rebase.
	tree, err := g.Tree(ctx, source.Repository, main)
	if err != nil {
		return nil, err
	}
	current := map[string]string{}
	for _, e := range tree {
		current[e.Path] = e.SHA
	}
	cached := map[string]string{}
	for _, f := range files {
		cached[f.Path] = f.BlobSHA
	}
	for path := range changed {
		if current[path] != cached[path] {
			return nil, fail(409, "stale_source", "The rule file or TL;DR changed on main. Update the source pin and reload.")
		}
	}
	// Reserve before the first GitHub mutation; same input recovers failures.
	err = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO doctrine_proposals(tenant_id,id,source_id,repository,path,rule_key,input_digest,base_commit,proposed_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(tenant_id,id) DO NOTHING`, actor.TenantID, in.RequestID, in.SourceID, source.Repository, in.Path, in.RuleKey, inputDigest(in), main, actor.ID)
		if err != nil || result.RowsAffected() == 0 {
			return err
		}
		_, err = events.Append(ctx, tx, actor, events.Change{Type: "doctrine.proposal_started", After: map[string]any{"proposal_id": in.RequestID, "repository": source.Repository, "proposed_by": actor.ID}})
		return err
	})
	if err != nil {
		return nil, err
	}
	var p Proposal
	err = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		var err error
		p, err = getProposal(ctx, tx, in.RequestID)
		if err != nil {
			return err
		}
		if p.InputDigest != inputDigest(in) || p.ProposedBy != actor.ID {
			return fail(409, "request_conflict", "That request UUID belongs to another proposal.")
		}
		if p.PRNumber > 0 {
			return nil
		}
		// A changed base after a partial request must be inspected, not rebased
		// underneath a possibly already published proposal branch.
		if p.BaseCommit != main {
			return fail(409, "base_changed", "Main moved during an unfinished proposal. Inspect its branch before starting a new request.")
		}
		if err = g.preparePR(ctx, &p, changed, in.Explanation); err != nil {
			return err
		}
		return saveProposal(ctx, tx, actor, p, "doctrine.proposed")
	})
	return p, err
}

func (m *Module) refreshProposal(r *http.Request, actor tenant.Principal) (any, error) {
	return m.withProposal(r, actor, "rules.write", func(ctx context.Context, tx pgx.Tx, g *GitHub, p *Proposal) error {
		previousRelease := p.ReleaseCommit
		if err := m.observe(ctx, g, p); err != nil {
			return err
		}
		if err := countPins(ctx, tx, p); err != nil {
			return err
		}
		event := "doctrine.proposal_refreshed"
		if p.ReleaseCommit != "" && previousRelease == "" {
			event = "doctrine.released"
		}
		return saveProposal(ctx, tx, actor, *p, event)
	})
}
func (m *Module) observe(ctx context.Context, g *GitHub, p *Proposal) error {
	if p.PRNumber == 0 {
		return nil
	}
	pr, err := g.pr(ctx, *p)
	if err != nil {
		return err
	}
	p.GateReady = false
	p.GateReason = ""
	if !validPull(*p, pr) {
		p.GateReason = "The PR head or target changed; inspect it in git."
		return nil
	}
	if pr.Merged {
		if !shaPattern.MatchString(pr.MergeCommit) {
			return gitFail("the merge commit is invalid")
		}
		if p.MergeCommit != "" && p.MergeCommit != pr.MergeCommit {
			return gitFail("the merge commit changed")
		}
		p.MergeCommit = pr.MergeCommit
		p.State = "merged"
		return g.observeRelease(ctx, p)
	}
	if pr.State == "closed" {
		p.State = "closed"
		return nil
	}
	review, reason, err := g.gate(ctx, *p, pr, m.app.GateLogin)
	if err != nil {
		return err
	}
	p.State = "in_review"
	p.GateReason = reason
	p.GateReady = review > 0
	return nil
}

// withProposal authorizes before credential reads and holds the proposal row
// lock across the bounded external operation, serializing retries per tenant.
func (m *Module) withProposal(r *http.Request, actor tenant.Principal, permission string, fn func(context.Context, pgx.Tx, *GitHub, *Proposal) error) (any, error) {
	if err := m.proposalAccess(actor); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	var p Proposal
	err := m.tx(ctx, actor, permission, func(tx pgx.Tx) error {
		var err error
		p, err = getProposal(ctx, tx, r.PathValue("proposalId"))
		if err != nil {
			return err
		}
		g, err := m.appClient(ctx, actor.TenantID, p.Repository)
		if err != nil {
			return err
		}
		return fn(ctx, tx, g, &p)
	})
	return p, err
}
func (m *Module) approveProposal(r *http.Request, actor tenant.Principal) (any, error) {
	if actor.Kind != tenant.Person || actor.KeyCreatorID != "" {
		return nil, authz.ErrForbidden
	}
	var in struct {
		Head string `json:"head_sha"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !shaPattern.MatchString(in.Head) {
		return nil, fail(400, "invalid_request", "Name the exact PR commit to approve.")
	}
	// Approval intent survives an uncertain merge response. No caller assertion
	// can stand in for the independent gate read from GitHub.
	_, err := m.withProposal(r, actor, "rules.publish", func(ctx context.Context, tx pgx.Tx, g *GitHub, p *Proposal) error {
		if in.Head != p.HeadSHA {
			return fail(409, "stale_head", "Reload the PR before approving its current commit.")
		}
		if p.ApprovedBy != "" {
			return nil
		}
		pr, err := g.pr(ctx, *p)
		if err != nil {
			return err
		}
		review, reason, err := g.gate(ctx, *p, pr, m.app.GateLogin)
		if err != nil {
			return err
		}
		if review == 0 {
			return fail(409, "gate_closed", reason)
		}
		p.ApprovedBy = actor.ID
		p.GateReview = review
		return saveProposal(ctx, tx, actor, *p, "doctrine.approved")
	})
	if err != nil {
		return nil, err
	}
	// Check again immediately before the SHA-guarded merge. The repository's
	// required checks and review protections are the final race-safe authority.
	_, err = m.withProposal(r, actor, "rules.publish", func(ctx context.Context, tx pgx.Tx, g *GitHub, p *Proposal) error {
		if in.Head != p.HeadSHA {
			return fail(409, "stale_head", "The approved PR commit changed.")
		}
		pr, err := g.pr(ctx, *p)
		if err != nil {
			return err
		}
		if !validPull(*p, pr) {
			return fail(409, "stale_head", "The PR head or target changed.")
		}
		if pr.Merged {
			if p.MergeCommit == pr.MergeCommit {
				return countPins(ctx, tx, p)
			}
			if !shaPattern.MatchString(pr.MergeCommit) {
				return gitFail("the merge commit is invalid")
			}
			p.MergeCommit = pr.MergeCommit
			p.State = "merged"
		} else {
			review, reason, err := g.gate(ctx, *p, pr, m.app.GateLogin)
			if err != nil {
				return err
			}
			if review == 0 {
				return fail(409, "gate_closed", reason)
			}
			p.GateReview = review
			if err = g.merge(ctx, p); err != nil {
				return err
			}
		}
		p.GateReady = false
		return saveProposal(ctx, tx, actor, *p, "doctrine.merged")
	})
	if err != nil {
		return nil, err
	}
	return m.withProposal(r, actor, "rules.publish", func(ctx context.Context, tx pgx.Tx, g *GitHub, p *Proposal) error {
		if p.MergeCommit == "" {
			return fail(409, "not_merged", "The merge has not been confirmed.")
		}
		if p.ReleaseRequested {
			return countPins(ctx, tx, p)
		}
		if !p.ReleaseRequested {
			if err := g.releaseRequest(ctx, *p); err != nil {
				return err
			}
			p.ReleaseRequested = true
		}
		// A repository dispatch is a request, never evidence of a release.
		return saveProposal(ctx, tx, actor, *p, "doctrine.release_requested")
	})
}
func (m *Module) reportPin(r *http.Request, actor tenant.Principal) (any, error) {
	if err := m.proposalAccess(actor); err != nil {
		return nil, err
	}
	var in struct {
		Machine string `json:"machine_key"`
		Commit  string `json:"commit"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(in.Machine) || !shaPattern.MatchString(in.Commit) {
		return nil, fail(400, "invalid_request", "Provide the machine identity digest and exact release commit.")
	}
	var p Proposal
	err := m.tx(r.Context(), actor, "settings.manage", func(tx pgx.Tx) error {
		var err error
		p, err = getProposal(r.Context(), tx, r.PathValue("proposalId"))
		if err != nil {
			return err
		}
		if p.ReleaseCommit == "" || in.Commit != p.ReleaseCommit {
			return fail(409, "release_mismatch", "Only an observed release commit can be reported as pinned.")
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO doctrine_machine_pins(tenant_id,repository,machine_key,commit_sha,reported_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,repository,machine_key) DO UPDATE SET commit_sha=EXCLUDED.commit_sha,reported_by=EXCLUDED.reported_by,reported_at=clock_timestamp()`, actor.TenantID, p.Repository, in.Machine, in.Commit, actor.ID)
		if err != nil {
			return err
		}
		if err = countPins(r.Context(), tx, &p); err != nil {
			return err
		}
		return saveProposal(r.Context(), tx, actor, p, "doctrine.machine_pin_reported")
	})
	return p, err
}

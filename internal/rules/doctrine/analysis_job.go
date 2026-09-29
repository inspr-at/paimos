// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type analysisAuthorityKey struct{}
type analysisAuthority struct {
	module      *Module
	principalID string
}

func (m *Module) analysisAuthorized(ctx context.Context, p tenant.Principal) bool {
	a, ok := ctx.Value(analysisAuthorityKey{}).(analysisAuthority)
	return ok && a.module == m && a.principalID == p.ID && p.Kind == tenant.Agent && p.TenantID == m.app.TenantID && m.app.configured()
}

// RunOutcomeAnalysis tries each UTC day once, including across process restarts
// and multiple server instances. Failed/uncertain draft attempts retain their
// reservation and request UUID for tomorrow's bounded retry. No LLM is called.
func (m *Module) RunOutcomeAnalysis(ctx context.Context) {
	if m.pool == nil || !m.app.configured() {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := m.analyzeOnce(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			// Errors can originate in externally supplied records. Log a fixed line,
			// never summaries, instruction prose or an upstream response body.
			slog.Error("doctrine outcome analysis incomplete; reserved drafts will retry on the next UTC day")
		}
		timer.Reset(time.Hour)
	}
}

func (m *Module) analyzeOnce(parent context.Context, now time.Time) error {
	if m.pool == nil || !m.app.configured() {
		return nil
	}
	ctx, cancel := context.WithTimeout(db.AllProjects(parent, "doctrine outcome analysis"), 10*time.Minute)
	defer cancel()
	tid := m.app.TenantID
	var actor tenant.Principal
	claimed := false
	day := now.UTC().Format("2006-01-02")
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO doctrine_analysis_runs(tenant_id,day) VALUES($1,$2::date) ON CONFLICT DO NOTHING`, tid, day)
		if err != nil || result.RowsAffected() == 0 {
			return err
		}
		claimed = true
		actor, err = systemactor.Ensure(ctx, tx, tid)
		return err
	})
	if err != nil || !claimed {
		return err
	}
	ctx = context.WithValue(ctx, analysisAuthorityKey{}, analysisAuthority{m, actor.ID})
	p := m.analysis.defaults()
	from := now.AddDate(0, 0, -p.WindowDays)
	var samples []analysisSample
	var existing []findingData
	err = m.tx(ctx, actor, "rules.read", func(tx pgx.Tx) error {
		var err error
		samples, err = loadAnalysisSamples(ctx, tx, tid, from, now)
		if err != nil {
			return err
		}
		existing, err = readFindings(ctx, tx, true)
		return err
	})
	if err != nil {
		return err
	}
	layer, err := m.load(ctx, actor, "rules.read")
	if err != nil {
		return err
	}
	// Existing reservations get first use of today's cap. Drafts are observed
	// but never made ready, approved, merged, released or reverted by the job.
	var first error
	for _, f := range existing {
		if f.Status == "draft" || f.Status == "awaiting_use" {
			err = m.observeFinding(ctx, actor, &f, samples, now)
		} else {
			err = m.attemptFinding(ctx, actor, &f, layer, day)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	candidates := detectFindings(samples, p, from, now)
	if len(candidates) > 100 {
		candidates = candidates[:100]
	}
	for _, f := range candidates {
		target, in, ok := analysisTarget(layer, f)
		data := findingData{finding: f}
		if !ok {
			data.Status = "internal_note"
			data.Reason = "No unambiguous matching doctrine rule is indexed."
		} else {
			data.SourceID, data.Path, data.RuleKey, data.RuleSHA = target.ID, in.Path, in.RuleKey, in.RuleSHA
			data.RuleLabel = ruleLabel(target, in)
		}
		reserved, err := m.reserveFinding(ctx, actor, &data)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if reserved && data.Status == "pending" {
			if err := m.attemptFinding(ctx, actor, &data, layer, day); err != nil && first == nil {
				first = err
			}
		}
	}
	err = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE doctrine_analysis_runs SET completed_at=$2 WHERE day=$1::date`, day, now)
		return err
	})
	if first != nil {
		return first
	}
	return err
}

func ruleLabel(source SourceView, in ProposalInput) string {
	for _, file := range source.Files {
		if file.Path == in.Path {
			for _, r := range file.Rules {
				if r.Key == in.RuleKey {
					if r.TLDR != nil && r.TLDR.EN != "" {
						return r.TLDR.EN
					}
					return patternTitle("gate:" + findingClass(r.Text))
				}
			}
		}
	}
	return "Doctrine rule"
}

// Prefer a single exact vocabulary match, never guess between rules. A
// source's full original rule remains intact; a fixed continuation proposes
// the smallest clarification through AEON-319's reword operation.
func analysisTarget(layer Layer, f finding) (SourceView, ProposalInput, bool) {
	class, advice := patternAdvice(f.Pattern)
	if class == "" {
		return SourceView{}, ProposalInput{}, false
	}
	type match struct {
		source SourceView
		in     ProposalInput
	}
	matches := []match{}
	for _, s := range layer.Sources {
		if s.State != "ready" || !writableSource(Source{Repository: s.Repository, Visibility: s.Visibility}) {
			continue
		}
		for _, file := range s.Files {
			if file.Problem != "" {
				continue
			}
			for _, r := range file.Rules {
				if findingClass(r.Text) != class || strings.Contains(r.Source, advice) {
					continue
				}
				in := ProposalInput{automatic: true, SourceID: s.ID, Path: file.Path, RuleKey: r.Key, RuleSHA: r.SHA256, Source: strings.TrimRight(r.Source, "\r\n") + "\n  " + advice + "\n", Explanation: proposalExplanation(f)}
				in.TLDR.EN = advice
				if r.TLDR != nil {
					in.TLDR.EN, in.TLDR.DE = r.TLDR.EN, r.TLDR.DE
				}
				matches = append(matches, match{s, in})
			}
		}
	}
	if len(matches) != 1 {
		return SourceView{}, ProposalInput{}, false
	}
	return matches[0].source, matches[0].in, true
}

func (m *Module) reserveFinding(ctx context.Context, actor tenant.Principal, f *findingData) (bool, error) {
	inserted := false
	err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		// Same workspace lock as manual proposal reservation. No lock is held
		// during GitHub I/O. The unique active-rule index is the final backstop.
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, actor.TenantID); err != nil {
			return err
		}
		if f.Status == "pending" {
			var open int
			var duplicate bool
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM doctrine_findings WHERE status IN ('pending','draft')`).Scan(&open); err != nil {
				return err
			}
			if open >= m.analysis.defaults().MaxOpenDrafts {
				return nil
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM doctrine_findings WHERE source_id=$1 AND path=$2 AND rule_key=$3 AND status IN ('pending','draft')) OR EXISTS(SELECT 1 FROM doctrine_proposals WHERE source_id=$1 AND path=$2 AND rule_key=$3 AND COALESCE(data->>'state','proposed') NOT IN ('closed','merged','released','pinned'))`, f.SourceID, f.Path, f.RuleKey).Scan(&duplicate); err != nil {
				return err
			}
			if duplicate {
				return nil
			}
		}
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO doctrine_findings(tenant_id,fingerprint,source_id,path,rule_key,status,data) VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7) ON CONFLICT DO NOTHING RETURNING id::text,created_at`, actor.TenantID, findingFingerprint(f.finding), f.SourceID, f.Path, f.RuleKey, f.Status, raw).Scan(&f.ID, &f.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		inserted = true
		return analysisEvent(ctx, tx, actor, f.finding, "doctrine.finding_created")
	})
	return inserted, err
}

func (m *Module) attemptFinding(ctx context.Context, actor tenant.Principal, f *findingData, layer Layer, day string) error {
	allowed := false
	err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE doctrine_analysis_runs SET attempts=attempts+1 WHERE day=$1::date AND attempts<$2`, day, m.analysis.defaults().DailyDraftCap)
		allowed = err == nil && result.RowsAffected() == 1
		return err
	})
	if err != nil || !allowed {
		return err
	}
	// Reconstruction must identify the same pinned rule and exact input after
	// a restart. Never reuse a request UUID for a new source version.
	source, in, ok := analysisTarget(layer, f.finding)
	if !ok || source.ID != f.SourceID || in.Path != f.Path || in.RuleKey != f.RuleKey || in.RuleSHA != f.RuleSHA {
		return m.noteFinding(ctx, actor, f, "The indexed rule changed or its mapping is ambiguous; inspect the reserved proposal.")
	}
	in.RequestID = f.ID
	// Store only the changed file hash. After measurements need a matching
	// instruction report; a repository pin or merged PR is not proof of use.
	var files []File
	err = m.tx(ctx, actor, "rules.read", func(tx pgx.Tx) error {
		s, err := getSource(ctx, tx, source.ID, false)
		if err != nil {
			return err
		}
		files, err = cachedFiles(ctx, tx, s)
		return err
	})
	if err != nil {
		return err
	}
	changed, editErr := editRule(Source{Repository: source.Repository, Commit: source.Commit, Visibility: source.Visibility}, files, in)
	if editErr == nil {
		f.AfterFileSHA = hashText(changed[in.Path])
	}
	result, err := m.proposeChange(ctx, actor, in)
	if err != nil {
		var failure *failure
		if errors.As(err, &failure) && (failure.Code == "private_doctrine" || failure.Code == "private_index_unavailable" || failure.Code == "public_identity" || failure.Code == "credential_text" || failure.Code == "non_latin") {
			return m.noteFinding(ctx, actor, f, "Kept internal by the doctrine leak guard ("+failure.Code+").")
		}
		// Unknown GitHub outcomes keep their slot; don't create an untracked draft
		// or silently turn a transport failure into a safe-to-forget internal note.
		f.Reason = "Draft outcome not confirmed; the same reservation will be retried."
		saveErr := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error { return saveFinding(ctx, tx, actor, *f, "doctrine.finding_pending") })
		if saveErr != nil {
			return saveErr
		}
		return err
	}
	proposal := result.(Proposal)
	f.ProposalID, f.PRURL, f.Status, f.Reason = proposal.ID, proposal.PRURL, "draft", ""
	return m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error { return saveFinding(ctx, tx, actor, *f, "doctrine.finding_proposed") })
}

func (m *Module) noteFinding(ctx context.Context, actor tenant.Principal, f *findingData, reason string) error {
	return m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		var reserved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM doctrine_proposals WHERE id=$1)`, f.ID).Scan(&reserved); err != nil {
			return err
		}
		if reserved {
			f.Reason = "A proposal was already reserved; inspect its branch before changing the rule mapping."
		} else {
			f.Status, f.Reason = "internal_note", reason
		}
		return saveFinding(ctx, tx, actor, *f, "doctrine.finding_internal")
	})
}

func afterMeasurement(samples []analysisSample, f findingData, now time.Time, windowDays, minSamples int) *analysisMetric {
	if f.AfterFileSHA == "" {
		return nil
	}
	from := now.AddDate(0, 0, -windowDays)
	if from.Before(f.Before.Until) {
		from = f.Before.Until
	}
	byVersion := map[string][]analysisSample{}
	for _, s := range samples {
		if s.Version == f.RulesVersion || s.Harness != f.Harness || s.TicketKind != f.TicketKind || !slices.Contains(s.FileHashes, f.AfterFileSHA) {
			continue
		}
		byVersion[s.Version] = append(byVersion[s.Version], s)
	}
	versions := []string{}
	for version := range byVersion {
		versions = append(versions, version)
	}
	slices.Sort(versions)
	for _, version := range versions {
		cohort := f.finding
		cohort.RulesVersion = version
		m := measure(byVersion[version], cohort, from, now)
		if m.Samples >= minSamples {
			return &m
		}
	}
	return nil
}

func (m *Module) observeFinding(ctx context.Context, actor tenant.Principal, f *findingData, samples []analysisSample, now time.Time) error {
	proposal, err := m.runProposal(ctx, actor, "rules.write", f.ProposalID, func(ctx context.Context, p *Proposal) (string, error) {
		g, err := m.appClient(ctx, actor.TenantID, p.Repository)
		if err != nil {
			return "", err
		}
		defer g.revoke()
		// observe is read-only: no readiness, approvals, merges or dispatches.
		return "doctrine.proposal_refreshed", m.observe(ctx, g, p)
	})
	if err != nil {
		return err
	}
	if proposal.State == "closed" {
		f.Status = "closed"
		return m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error { return saveFinding(ctx, tx, actor, *f, "doctrine.finding_closed") })
	}
	if proposal.MergeCommit == "" {
		return nil
	}
	// A merged change no longer consumes an open-draft slot. Keep following
	// its provenance separately until a meaningful comparison is possible.
	if f.Status != "awaiting_use" {
		f.Status = "awaiting_use"
		if err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error { return saveFinding(ctx, tx, actor, *f, "doctrine.finding_awaiting_use") }); err != nil {
			return err
		}
	}
	metric := afterMeasurement(samples, *f, now, m.analysis.defaults().WindowDays, m.analysis.defaults().MinOccurrences)
	if metric == nil {
		return nil
	}
	delta := metric.Value - f.Before.Value
	f.After, f.Delta, f.Status = metric, &delta, "observed"
	return m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		if err := saveFinding(ctx, tx, actor, *f, "doctrine.proposal_measured"); err != nil {
			return err
		}
		if len(f.Evidence) == 0 {
			return nil
		}
		ticketID := f.Evidence[0].TicketID
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, ticketID).Scan(&exists); err != nil || !exists {
			return err
		}
		body := fmt.Sprintf("Doctrine proposal outcome: %s. Before %.4f (%d observations); after %.4f (%d observations); delta %+.4f. Same harness and ticket kind, with matching instruction provenance. Descriptive comparison; no automatic revert. See Settings → Agent rules → Proposals.", f.Title, f.Before.Value, f.Before.Samples, metric.Value, metric.Samples, delta)
		_, err := events.Append(ctx, tx, actor, events.Change{NodeID: &ticketID, Type: "comment.created", After: map[string]any{"body_markdown": body}, Metadata: json.RawMessage(`{"job":"doctrine-outcome-analysis","reason":"before and after comparison"}`)})
		return err
	})
}

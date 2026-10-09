// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAnalysisPatternsAndMetrics(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	from := now.AddDate(0, 0, -14)
	samples := []analysisSample{}
	for i := 0; i < 4; i++ {
		for _, kind := range []string{"review_verdict", "fix_round", "ci_result", "revert", "ticket_done", "learning", "vote"} {
			s := analysisSample{analysisEvidence: analysisEvidence{ID: fmt.Sprintf("%s-%d", kind, i), TicketID: fmt.Sprint(i), Kind: kind}, Version: "v1", Harness: "codex", TicketKind: "ticket", Summary: "Missing regression test", Result: "changes", Round: 4, Elapsed: 300, At: now.Add(-time.Hour)}
			if kind == "ci_result" {
				s.Result = "fail"
			}
			if i == 3 {
				s.Result = "ok"
				s.Round = 1
				s.Summary = "All clear"
				s.Elapsed = 600
			}
			samples = append(samples, s)
		}
	}
	p := AnalysisPolicy{}.defaults()
	found := detectFindings(samples, p, from, now)
	byPattern := map[string]finding{}
	for _, f := range found {
		byPattern[f.Pattern] = f
	}
	if len(byPattern) != 6 {
		t.Fatalf("patterns = %v", byPattern)
	}
	for pattern, want := range map[string]float64{"gate:validation": .75, "fix_rounds": 3.25, "ci_failures": .75, "learning:validation": .75, "exception_votes": 1, "reverts": 1} {
		f, ok := byPattern[pattern]
		if !ok || math.Abs(f.Before.Value-want) > 1e-8 {
			t.Fatalf("%s metric: %+v", pattern, f)
		}
	}
	if len(byPattern["gate:validation"].Metrics) != 6 {
		t.Fatal("baseline lost supporting outcome metrics")
	}
	done := byPattern["fix_rounds"]
	done.Pattern = "time_to_done"
	if got := measure(samples, done, from, now); got.Value != 375 || got.Samples != 4 {
		t.Fatalf("time to done: %+v", got)
	}
	// Duplicate evidence and multiple rounds from one ticket do not fabricate N.
	repeats := []analysisSample{samples[0], samples[0], samples[0]}
	repeats[1].ID = "another-round"
	if len(detectFindings(repeats, p, from, now)) != 0 {
		t.Fatal("one ticket became a recurring failure")
	}
	mixed := append([]analysisSample(nil), samples...)
	for i := range mixed {
		mixed[i].Version = fmt.Sprint(i)
	}
	if len(detectFindings(mixed, p, from, now)) != 0 {
		t.Fatal("pooled unrelated rules versions")
	}
	for i := range mixed {
		mixed[i].Version = ""
	}
	if len(detectFindings(mixed, p, from, now)) != 0 {
		t.Fatal("assigned unattributed work")
	}
	// Provenance, harness, ticket kind, version and time must all match.
	f := findingData{finding: byPattern["gate:validation"], AfterFileSHA: strings.Repeat("f", 64)}
	after := append([]analysisSample(nil), samples...)
	for i := range after {
		after[i].Version = "v2"
		after[i].FileHashes = []string{f.AfterFileSHA}
		after[i].At = now.Add(time.Hour)
		after[i].Result = "ok"
	}
	got := afterMeasurement(after, f, now.Add(24*time.Hour), 14, 3)
	if got == nil || got.Value != 0 || got.Samples != 4 || got.RulesVersion != "v2" {
		t.Fatalf("after = %+v", got)
	}
	for _, field := range []string{"hash", "harness", "kind", "time", "version"} {
		t.Run(field, func(t *testing.T) {
			bad := append([]analysisSample(nil), after...)
			for i := range bad {
				switch field {
				case "hash":
					bad[i].FileHashes = nil
				case "harness":
					bad[i].Harness = "pi"
				case "kind":
					bad[i].TicketKind = "epic"
				case "time":
					bad[i].At = from
				case "version":
					bad[i].Version = "v1"
				}
			}
			if afterMeasurement(bad, f, now.Add(24*time.Hour), 14, 3) != nil {
				t.Fatal("unproven comparison")
			}
		})
	}
}

const analysisFixturePath = "docs/AGENTS-DOMAIN-DEV.md"
const analysisFixtureRules = "# Dev\n\n## Validation\n<!-- aeon-rule: validation -->\n- Run tests before delivery.\n\n## Scope\n<!-- aeon-rule: scope -->\n- Preserve the authorized scope.\n"

func setupAnalysis(t *testing.T) (doctrineFixture, *proposalForge, *Module, tenant.Principal) {
	t.Helper()
	f, forge, m := newProposalFixture(t)
	m.analysisLearnings = func(context.Context, pgx.Tx, string, string) (AnalysisLearningPage, error) {
		return AnalysisLearningPage{}, nil
	}
	tid := f.tenant("analysis")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	f.fake.commit(publicRepository, fixtureCommit, map[string]string{analysisFixturePath: analysisFixtureRules}, "main")
	f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	return f, forge, m, owner
}

func seedAnalysisOutcomes(t *testing.T, f doctrineFixture, p tenant.Principal, prefix, version, summary, result, fileSHA string, at time.Time) []string {
	t.Helper()
	ids := []string{}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, p.TenantID, func(tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,'Analysis fixtures' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING nodes.id::text`, p.TenantID, prefix+"-1").Scan(&project); err != nil {
			return err
		}
		for i := 0; i < 3; i++ {
			var ticket, session, provenance string
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,$2,'Outcome fixture',$3 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING nodes.id::text`, p.TenantID, fmt.Sprintf("%s-%d", prefix, i+2), project).Scan(&ticket); err != nil {
				return err
			}
			ids = append(ids, ticket)
			if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,ticket_node_id,work_shape,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,created_at) VALUES($1,$2,$3::uuid,'ship',$4,'codex','fixture','unmanaged','worker',decode(md5($3::uuid::text),'hex'),decode(md5('fixture'),'hex'),$5) RETURNING id::text`, p.TenantID, project, ticket, p.ID, at.Add(-time.Hour)).Scan(&session); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO harness_instruction_provenance(tenant_id,session_id,revision,set_digest,recorded_by,created_at) VALUES($1,$2,1,decode(repeat('a',64),'hex'),$3,$4) RETURNING id::text`, p.TenantID, session, p.ID, at.Add(-time.Minute)).Scan(&provenance); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO harness_instruction_provenance_items(tenant_id,provenance_id,ordinal,kind,logical_name,hash_kind,content_sha256,version,byte_size) VALUES($1,$2,0,'rules_merged','merged-rules','content',repeat('b',64),$3,100),($1,$2,1,'agents','AGENTS.md','content',$4,NULL,100)`, p.TenantID, provenance, version, fileSHA); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"summary": summary, "verdict": result, "round": 1})
			if _, err := tx.Exec(t.Context(), `INSERT INTO outcome_events(tenant_id,kind,project_id,ticket_node_id,session_id,rules_version,idempotency_key,actor_principal_id,source,payload,request_digest,recorded_at) VALUES($1,'review_verdict',$2,$3,$4,$5,$6,$7,'recorded',$8,decode(md5($6),'hex'),$9)`, p.TenantID, project, ticket, session, version, "analysis-"+ticket, p.ID, payload, at); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func readAnalysis(t *testing.T, f doctrineFixture, p tenant.Principal) []finding {
	t.Helper()
	var out struct {
		Findings []finding `json:"findings"`
	}
	if err := json.Unmarshal(f.call(p, "GET", "/api/rules/doctrine/analysis", nil, 200), &out); err != nil {
		t.Fatal(err)
	}
	return out.Findings
}

// afterFileSHA is the stored file hash a finding's after measurement needs.
func afterFileSHA(t *testing.T, f doctrineFixture, id string) string {
	t.Helper()
	var sha string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT COALESCE(data->>'after_file_sha','') FROM doctrine_findings WHERE id=$1`, id).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	return sha
}

// inboxProposals counts the doctrine inbox proposals of a workspace.
func inboxProposals(t *testing.T, f doctrineFixture, tid string) int {
	t.Helper()
	var n int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_proposals WHERE tenant_id=$1 AND data->>'inbox'='true'`, tid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The outcome job never publishes (AEON-444): its proposal waits in the
// doctrine inbox, proposed by System, and nothing reaches GitHub until a
// person sends it to git.
func TestAnalysisDraftJobAndBeforeAfter(t *testing.T) {
	f, forge, m, owner := setupAnalysis(t)
	now := time.Now().UTC().Add(time.Minute)
	tickets := seedAnalysisOutcomes(t, f, owner, "BEFORE", "v1", "Missing regression test", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	writes, calls := forge.writes, forge.calls
	concurrent := make(chan error, 2)
	for range 2 {
		go func() { concurrent <- m.analyzeOnce(t.Context(), now) }()
	}
	for range 2 {
		if err := <-concurrent; err != nil {
			t.Fatal(err)
		}
	}
	got := readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "draft" || got[0].Count != 3 || got[0].Before.Value != 1 || len(got[0].Evidence) != 3 || got[0].ProposalID != got[0].ID || got[0].PRURL != "" {
		t.Fatalf("finding = %+v", got)
	}
	if len(forge.pulls) != 0 || forge.writes != writes || forge.calls != calls || forge.labels != 0 || forge.mergeCalls != 0 || forge.dispatches != 0 {
		t.Fatal("the outcome job reached GitHub; only a person publishes")
	}
	const path = "/api/rules/doctrine/inbox"
	var inbox inboxList
	raw := f.call(owner, "GET", path, nil, 200)
	if err := json.Unmarshal(raw, &inbox); err != nil {
		t.Fatal(err)
	}
	if inbox.Pending != 1 || len(inbox.Items) != 1 || inbox.Items[0].ID != got[0].ProposalID || inbox.Items[0].State != "pending" || inbox.Items[0].Proposer != "System" || inbox.Items[0].PRNumber != 0 {
		t.Fatalf("the job's proposal must wait in the inbox: %+v", inbox)
	}
	for _, private := range []string{"Missing regression test", owner.TenantID, tickets[0]} {
		if strings.Contains(inbox.Items[0].Proposed+inbox.Items[0].Why, private) {
			t.Fatal("private evidence reached the proposed text")
		}
	}
	if err := m.analyzeOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if forge.calls != calls {
		t.Fatal("same-day restart bypassed daily cap")
	}
	// An agent, including a spoofed System name/role, has no analysis access.
	agent := f.principal(owner.TenantID, "agent", "System", "admin", []string{"rules.read", "rules.write"}, owner.ID)
	f.call(agent, "GET", "/api/rules/doctrine/analysis", nil, 403)
	f.call(agent, "GET", "/api/rules/doctrine/proposals", nil, 403)
	foreign := f.principal(f.tenant("foreign-analysis"), "person", "owner", "admin", nil, "")
	if len(readAnalysis(t, f, foreign)) != 0 {
		t.Fatal("cross-tenant findings")
	}
	// A person sends it to git; the PR is theirs, not the job's.
	var sent Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path+"/"+got[0].ProposalID+"/pull-request", map[string]any{}, 200), &sent)
	if sent.PRNumber == 0 || sent.SubmittedBy != owner.ID || len(forge.pulls) != 1 || forge.labels != 0 {
		t.Fatalf("a person's PR: %+v", sent)
	}
	for _, body := range forge.bodies {
		if strings.Contains(body, "Missing regression test") || strings.Contains(body, owner.TenantID) || strings.Contains(body, tickets[0]) {
			t.Fatal("private evidence left the workspace")
		}
	}
	// A person merges externally. The job only observes that action and waits
	// for actual use of the changed instruction bytes before measuring.
	for key, p := range forge.pulls {
		p.Merged = true
		p.State = "closed"
		p.Draft = false
		p.MergeCommit = privateSHA
		forge.pulls[key] = p
	}
	next := now.Add(24 * time.Hour)
	if err := m.analyzeOnce(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	got = readAnalysis(t, f, owner)
	if got[0].Status != "awaiting_use" || got[0].After != nil || got[0].PRURL != sent.PRURL {
		t.Fatal("merged pin was treated as used instruction bytes")
	}
	next = next.Add(24 * time.Hour)
	seedAnalysisOutcomes(t, f, owner, "AFTER", "v2", "Checks passed", "ok", hashText(forge.treeFiles[analysisFixturePath]), next.Add(-time.Hour))
	if err := m.analyzeOnce(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	got = readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "observed" || got[0].After == nil || got[0].Delta == nil || *got[0].Delta != -1 {
		t.Fatalf("measurement = %+v", got)
	}
	if forge.mergeCalls != 0 || forge.dispatches != 0 {
		t.Fatal("job published a change")
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, owner.TenantID, func(tx pgx.Tx) error {
		var wrong, comments, published int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE (e.type LIKE 'doctrine.finding_%' OR e.type IN ('doctrine.inbox_proposed','doctrine.proposal_measured') OR e.metadata->>'job'='doctrine-outcome-analysis') AND NOT (p.name='System' AND p.roles @> ARRAY['system'])`).Scan(&wrong); err != nil {
			return err
		}
		// Publication is the person's, never System's.
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='doctrine.proposed' AND actor_principal_id<>$1`, owner.ID).Scan(&published); err != nil {
			return err
		}
		if published != 0 {
			t.Fatalf("%d publication events by someone other than the person", published)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='comment.created' AND metadata->>'job'='doctrine-outcome-analysis'`).Scan(&comments); err != nil {
			return err
		}
		if wrong != 0 || comments != 1 {
			t.Fatalf("actor violations %d; metric comments %d", wrong, comments)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A person who edits the job's proposal before sending it approves other
// bytes than the job drafted: the after measurement follows the file they
// sent, not the file the job computed.
func TestAnalysisEditedProposalMeasuresTheApprovedFile(t *testing.T) {
	f, forge, m, owner := setupAnalysis(t)
	now := time.Now().UTC().Add(time.Minute)
	seedAnalysisOutcomes(t, f, owner, "BEFORE", "v1", "Missing regression test", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	if err := m.analyzeOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	got := readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "draft" || afterFileSHA(t, f, got[0].ID) == "" {
		t.Fatalf("finding = %+v", got)
	}
	drafted := afterFileSHA(t, f, got[0].ID)
	const path = "/api/rules/doctrine/inbox"
	var inbox inboxList
	_ = json.Unmarshal(f.call(owner, "GET", path, nil, 200), &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].Proposed == "" {
		t.Fatalf("inbox = %+v", inbox)
	}
	item := inbox.Items[0]
	edit := map[string]any{"source": strings.TrimRight(item.Proposed, "\n") + " Name the command you ran.", "rule_sha256": item.BaseSHA}
	var sent Proposal
	_ = json.Unmarshal(f.call(owner, "POST", path+"/"+item.ID+"/pull-request", edit, 200), &sent)
	approved := hashText(forge.treeFiles[analysisFixturePath])
	if sent.PRNumber == 0 || sent.EditedBy != owner.ID || !strings.Contains(forge.treeFiles[analysisFixturePath], "Name the command you ran.") {
		t.Fatalf("edited send: %+v", sent)
	}
	if sent.ApprovedFileSHA != approved || approved == drafted {
		t.Fatalf("approved file %q, sent %q, drafted %q", approved, sent.ApprovedFileSHA, drafted)
	}
	for key, p := range forge.pulls {
		p.Merged, p.State, p.Draft, p.MergeCommit = true, "closed", false, privateSHA
		forge.pulls[key] = p
	}
	next := now.Add(24 * time.Hour)
	if err := m.analyzeOnce(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	got = readAnalysis(t, f, owner)
	if got[0].Status != "awaiting_use" || afterFileSHA(t, f, got[0].ID) != approved || got[0].PRURL != sent.PRURL {
		t.Fatalf("the measurement kept the drafted bytes: %+v", got[0])
	}
	// Agents now report the approved file; the comparison completes.
	next = next.Add(24 * time.Hour)
	seedAnalysisOutcomes(t, f, owner, "AFTER", "v2", "Checks passed", "ok", approved, next.Add(-time.Hour))
	if err := m.analyzeOnce(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	got = readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "observed" || got[0].After == nil || got[0].Delta == nil || *got[0].Delta != -1 {
		t.Fatalf("measurement = %+v", got)
	}
}

// A proposal the job could not record keeps its reservation; the next day
// records it once, under the same request id.
func TestAnalysisRetriesAnUnrecordedProposal(t *testing.T) {
	f, forge, m, owner := setupAnalysis(t)
	now := time.Now().UTC().Add(time.Minute)
	seedAnalysisOutcomes(t, f, owner, "RETRY", "v1", "Missing regression test", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	guard := m.guardMaster
	m.guardMaster = nil
	if err := m.analyzeOnce(t.Context(), now); err != nil {
		t.Fatalf("unavailable guard should leave a quiet pending finding: %v", err)
	}
	got := readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "pending" || inboxProposals(t, f, owner.TenantID) != 0 {
		t.Fatal("a failed attempt lost its reservation or left a proposal")
	}
	if got[0].Reason != "The private instruction check is unavailable; no draft was published." {
		t.Fatalf("misleading unavailable reason: %q", got[0].Reason)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, owner.TenantID, func(tx pgx.Tx) error {
		var attempts int
		if err := tx.QueryRow(t.Context(), `SELECT attempts FROM doctrine_analysis_runs WHERE day=$1::date`, now.Format("2006-01-02")).Scan(&attempts); err != nil {
			return err
		}
		if attempts != 0 {
			t.Fatalf("unavailable guard consumed %d daily attempts", attempts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	id := got[0].ID
	m.guardMaster = guard
	for _, day := range []time.Duration{24 * time.Hour, 48 * time.Hour} {
		if err := m.analyzeOnce(t.Context(), now.Add(day)); err != nil {
			t.Fatal(err)
		}
	}
	got = readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].ID != id || got[0].Status != "draft" || got[0].ProposalID != id || inboxProposals(t, f, owner.TenantID) != 1 || forge.writes != 0 {
		t.Fatal("the retry duplicated the proposal or published it")
	}
}

func TestAnalysisCapsAndRuleDedupe(t *testing.T) {
	f, _, m, owner := setupAnalysis(t)
	m.analysis.MaxOpenDrafts = 1
	ctx := db.AllProjects(t.Context(), "analysis test")
	var actor tenant.Principal
	if err := db.InTenant(ctx, f.d.App, owner.TenantID, func(tx pgx.Tx) error {
		var err error
		actor, err = systemactor.Ensure(ctx, tx, owner.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx = context.WithValue(ctx, analysisAuthorityKey{}, analysisAuthority{m, actor.ID})
	layer, err := m.load(ctx, actor, "rules.read")
	if err != nil {
		t.Fatal(err)
	}
	first := finding{Pattern: "gate:validation", RulesVersion: "v1", Harness: "codex", TicketKind: "ticket", Count: 3, Status: "pending"}
	src, in, ok := New(nil, Options{}).analysisTarget(layer, first)
	if !ok {
		t.Fatal("fixture mapping")
	}
	data := findingData{finding: first, SourceID: src.ID, Path: in.Path, RuleKey: in.RuleKey}
	if inserted, err := m.reserveFinding(ctx, actor, &data); err != nil || !inserted {
		t.Fatalf("reservation %v %v", inserted, err)
	}
	second := data
	second.RulesVersion = "v2"
	if inserted, err := m.reserveFinding(ctx, actor, &second); err != nil || inserted {
		t.Fatal("duplicate rule admitted")
	}
	second.Pattern = "gate:scope"
	second.RuleKey = "scope"
	if inserted, err := m.reserveFinding(ctx, actor, &second); err != nil || inserted {
		t.Fatal("open draft cap exceeded")
	}
	// The cap and unique rule index remain tenant-local.
	m.analysis.MaxOpenDrafts = 2
	if inserted, err := m.reserveFinding(ctx, actor, &second); err != nil || !inserted {
		t.Fatal("second distinct rule not admitted")
	}
	f.layer(owner, "DELETE", "/api/rules/doctrine/sources/"+src.ID, nil)
	if len(readAnalysis(t, f, owner)) != 2 {
		t.Fatal("source removal discarded proposal history")
	}
}

func TestAnalysisPrivateQuoteBecomesInternalNote(t *testing.T) {
	f, forge, m, owner := setupAnalysis(t)
	now := time.Now().UTC().Add(time.Minute)
	seedAnalysisOutcomes(t, f, owner, "PRIVATE", "v1", "Missing regression test", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	// The generated advice is present only in private material. Keep the
	// source out of the visible rule index to avoid a second target match.
	_, advice := patternAdvice("gate:validation")
	f.fake.commit(privateRepository, nextCommit, map[string]string{
		"docs/AGENTS-KERNEL-PRIVATE.md": "# Private\n\n## Planning\n- Keep notebooks under the stairway.\n",
		"private-notes.txt":             advice,
	}, "main")
	layer := f.layer(owner, "GET", "/api/rules/doctrine", nil)
	priv := find(layer, privateRepository)
	f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+priv.ID, SourceInput{Visibility: "private", Commit: nextCommit, CredentialRef: "guard-read"})
	writes := forge.writes
	if err := m.analyzeOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	got := readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "internal_note" || !strings.Contains(got[0].Reason, "private instruction") {
		t.Fatalf("note = %+v", got)
	}
	if len(forge.pulls) != 0 || forge.writes != writes {
		t.Fatal("private quotation caused a GitHub write")
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), advice) {
		t.Fatal("private quotation stored in the finding")
	}
}

func TestAnalysisDailyAttemptCap(t *testing.T) {
	f, forge, m, owner := setupAnalysis(t)
	m.analysis.DailyDraftCap = 1
	now := time.Now().UTC().Add(time.Minute)
	seedAnalysisOutcomes(t, f, owner, "VALIDATE", "v1", "Missing regression test", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	seedAnalysisOutcomes(t, f, owner, "SCOPE", "v1", "Unrelated scope change", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	if err := m.analyzeOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	got := readAnalysis(t, f, owner)
	pending, drafts := 0, 0
	for _, f := range got {
		if f.Status == "draft" {
			drafts++
		}
		if f.Status == "pending" {
			pending++
		}
	}
	if drafts != 1 || pending != 1 || inboxProposals(t, f, owner.TenantID) != 1 || len(forge.pulls) != 0 {
		t.Fatalf("daily cap: %d drafts, %d pending", drafts, pending)
	}
	if err := m.analyzeOnce(t.Context(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if inboxProposals(t, f, owner.TenantID) != 2 || len(forge.pulls) != 0 {
		t.Fatal("next day did not recover reserved second rule")
	}
}

func TestAnalysisWaitsForPrivateGuard(t *testing.T) {
	f, forge, m, owner := setupAnalysis(t)
	now := time.Now().UTC().Add(time.Minute)
	layer := f.layer(owner, "GET", "/api/rules/doctrine", nil)
	f.layer(owner, "DELETE", "/api/rules/doctrine/sources/"+find(layer, privateRepository).ID, nil)
	seedAnalysisOutcomes(t, f, owner, "GUARD", "v1", "Missing regression test", "changes", strings.Repeat("c", 64), now.Add(-time.Hour))
	if err := m.analyzeOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	got := readAnalysis(t, f, owner)
	if len(got) != 1 || got[0].Status != "pending" || forge.writes != 0 {
		t.Fatal("missing guard neither held nor blocked writes")
	}
	seedPrivateGuard(t, f, m, owner)
	if err := m.analyzeOnce(t.Context(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = readAnalysis(t, f, owner)
	if got[0].Status != "draft" || inboxProposals(t, f, owner.TenantID) != 1 || len(forge.pulls) != 0 {
		t.Fatal("rebuilt guard did not resume the reserved proposal")
	}
}

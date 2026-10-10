// SPDX-License-Identifier: AGPL-3.0-only
package doctrine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Handler-level: fixture.call injects a principal and skips auth middleware.
// Actual bearer-key refusals are pinned separately in internal/auth.
func TestPoliciesDoctrineHandlerUnlockedAgentAndLockedHumanBoundary(t *testing.T) {
	f, _, m := newProposalFixture(t)
	tid := f.tenant("policies-doctrine")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "Owner", "admin", nil, "")
	agent := f.principal(tid, "agent", "Agent", "admin", []string{"rules.read", "rules.write", "nodes.read"}, owner.ID)
	layer := f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"})
	seedPrivateGuard(t, f, m, owner)
	src := find(layer, publicRepository)
	inboxTicket(t, f, owner)
	file, rule := ruleWith(t, src, "Small commits.")
	unlocked := InboxInput{RequestID: "44400000-0000-4000-8000-000000000011", Repository: publicRepository, Path: file.Path, RuleKey: rule.Key, Source: strings.Replace(rule.Source, "Small commits.", "Small reviewed commits.", 1), Why: "Pin the existing handler boundary.", Ticket: "INB-2", TLDR: &inboxTLDR{EN: "Keep commits small and reviewed."}}
	var proposal Proposal
	if err := json.Unmarshal(f.call(agent, "POST", "/api/rules/doctrine/inbox", unlocked, 200), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.ProposedBy != agent.ID || !proposal.Inbox || proposal.State != "pending" {
		t.Fatal("unlocked handler did not accept agent principal")
	}
	var before int
	count := func(dst *int) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_proposals`).Scan(dst)
		}); err != nil {
			t.Fatal(err)
		}
	}
	count(&before)
	lockedFile, lockedRule := ruleWith(t, src, "NEVER")
	locked := InboxInput{RequestID: "44400000-0000-4000-8000-000000000012", SourceID: src.ID, Path: lockedFile.Path, RuleKey: lockedRule.Key, Source: strings.Replace(lockedRule.Source, "run `env`", "run `env` or `printenv`", 1), Why: "Pin locked rule refusal."}
	for _, p := range []tenant.Principal{agent, {ID: owner.ID, TenantID: tid, Kind: tenant.Person, KeyCreatorID: owner.ID}} {
		var body map[string]any
		if err := json.Unmarshal(f.call(p, "POST", "/api/rules/doctrine/inbox", locked, 403), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "locked_rule" {
			t.Fatalf("wrong refusing handler: %v", body["code"])
		}
	}
	var after int
	count(&after)
	if after != before {
		t.Fatal("locked refusal wrote inbox proposal")
	}
	if !humanActor(owner) || humanActor(agent) {
		t.Fatal("human actor boundary changed")
	}
}

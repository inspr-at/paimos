// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const guardRule = "The copper observatory keeps seven violet notebooks beneath the eastern stairway for seasonal planning."
const guardTLDR = "Copper notebooks stay hidden."

func seedPrivateGuard(t *testing.T, f doctrineFixture, m *Module, actor tenant.Principal) SourceView {
	t.Helper()
	f.fake.commit(privateRepository, fixtureCommit, map[string]string{
		"docs/AGENTS-KERNEL-PRIVATE.md":        "# Private\n\n## Planning\n<!-- aeon-rule: copper -->\n- " + guardRule + "\n",
		"docs/AGENTS-KERNEL-PRIVATE.tldr.yaml": "rules:\n  copper:\n    en: " + guardTLDR + "\n    de: Kupferne Notizen bleiben verborgen.\n  unmatched:\n    en: Reserved observatory protocol.\n",
	}, "main")
	allowCredential(t, m.credentials.Dir, "guard-read", actor.TenantID, privateRepository)
	if err := os.WriteFile(filepath.Join(m.credentials.Dir, "guard-read"), []byte("fixtureGuardRead319"), 0600); err != nil {
		t.Fatal("write fixture credential")
	}
	return *find(f.layer(actor, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: privateRepository, Visibility: "private", Ref: "main", CredentialRef: "guard-read"}), privateRepository)
}

func publicProposalFixture(t *testing.T) (doctrineFixture, *proposalForge, *Module, tenant.Principal, ProposalInput) {
	t.Helper()
	f, forge, m := newProposalFixture(t)
	tid := f.tenant("proposal-fix")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.Dir, "app-key", tid, publicRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	src := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: publicRepository, Visibility: "public", Ref: "main"}), publicRepository)
	rule := src.Files[1].Rules[0]
	in := ProposalInput{RequestID: "31900000-0000-4000-8000-000000000009", SourceID: src.ID, Path: src.Files[1].Path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: rule.Source, Explanation: "Clarify transcript hygiene."}
	in.TLDR.EN = "Keep credentials out of transcripts."
	return f, forge, m, owner, in
}

func fullwidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '!' && r <= '~' {
			return r + 0xfee0
		}
		return r
	}, s)
}

func TestPrivateQuoteNormalization(t *testing.T) {
	corpus := []string{guardRule, guardTLDR}
	for _, quote := range []string{
		guardRule, guardTLDR, "Notice: " + strings.ToUpper(guardRule),
		strings.ReplaceAll(guardRule, "o", "o\u200b"), fullwidth(guardRule),
		"Please keep " + strings.Join(strings.Fields(guardRule)[3:11], " ") + " safe.",
		strings.Replace(guardRule, "beneath", "under", 1),
	} {
		if err := guardPrivateQuotes(corpus, quote); err == nil {
			t.Errorf("private quotation accepted: %q", quote)
		}
	}
	for _, public := range []string{"Run tests before merging.", "The observatory publishes public release notes.", "Keep credentials out of transcripts."} {
		if err := guardPrivateQuotes(corpus, public); err != nil {
			t.Fatalf("public edit rejected: %v", err)
		}
	}
	for _, text := range []string{"hsb\u200b1", "ｈｓｂ１", "barta\u200b.cm", "h\u034fsb1", "hsb\ufe0f1"} {
		if guardPublic(publicRepository, text) == nil {
			t.Errorf("normalized pattern missed: %q", text)
		}
	}
}

func TestPublicGuardOnlyChangedText(t *testing.T) {
	content := "# Kernel\n\n## Rules\n<!-- aeon-rule: edit -->\n- Run tests.\n<!-- aeon-rule: existing -->\n- Existing operator@example.test reference.\n"
	path := "docs/AGENTS-KERNEL.md"
	files := []File{{Path: path, Content: []byte(content)}, {Path: SidecarPath(path), Content: []byte("rules:\n  existing: {en: 'Existing operator@example.test note.'}\n")}}
	rule := Render(publicRepository, fixtureCommit, false, files)[0].Rules[0]
	in := ProposalInput{Path: path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: strings.Replace(rule.Source, "Run tests.", "Run tests before merging.", 1), Explanation: "Clarify validation."}
	in.TLDR.EN = "Validate before merging."
	if _, err := editRule(Source{Repository: publicRepository, Commit: fixtureCommit}, files, in); err != nil {
		t.Fatalf("unchanged content blamed: %v", err)
	}
}

func TestPublicProposalRequiresPrivateCorpusAndBlocksQuotes(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	endpoint := "/api/rules/doctrine/proposals"
	before := forge.calls
	if got := f.call(owner, "POST", endpoint, in, 422); !strings.Contains(string(got), "private_index_unavailable") || forge.calls != before {
		t.Fatal("missing private index did not fail before GitHub")
	}
	private := seedPrivateGuard(t, f, m, owner)
	for _, field := range []string{"rule", "en", "de", "explanation", "unmatched"} {
		bad := in
		switch field {
		case "rule":
			bad.Source = "<!-- aeon-rule: no-env-dump -->\n- " + fullwidth(guardRule) + "\n"
		case "en":
			bad.TLDR.EN = guardTLDR
		case "de":
			bad.TLDR.DE = "Kupferne Notizen bleiben verborgen."
		case "explanation":
			bad.Explanation = strings.ReplaceAll(guardRule, "o", "o\u200b")
		case "unmatched":
			bad.Explanation = "Reserved observatory protocol."
		}
		before = forge.calls
		got := f.call(owner, "POST", endpoint, bad, 422)
		if !strings.Contains(string(got), "private_doctrine") || strings.Contains(string(got), guardTLDR) || forge.calls != before {
			t.Fatalf("private %s reached GitHub or was reflected", field)
		}
	}
	allowCredential(t, m.credentials.Dir, "guard-read", owner.TenantID, publicRepository)
	before = forge.calls
	f.call(owner, "POST", endpoint, in, 422)
	if forge.calls != before {
		t.Fatal("revoked private grant reached GitHub")
	}
	allowCredential(t, m.credentials.Dir, "guard-read", owner.TenantID, privateRepository)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_sources SET index_error='failed' WHERE id=$1`, private.ID); err != nil {
		t.Fatal(err)
	}
	f.call(owner, "POST", endpoint, in, 422)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_sources SET index_error='' WHERE id=$1`, private.ID); err != nil {
		t.Fatal(err)
	}
	f.call(owner, "POST", endpoint, in, 200)
	if forge.minted != forge.revocations {
		t.Fatal("successful proposal left installation token live")
	}
}

// Execute the request asynchronously without using Fatal in its goroutine.
func asyncProposal(f doctrineFixture, actor tenant.Principal, endpoint string, in any) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		raw, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", endpoint, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), actor))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		done <- w
	}()
	return done
}

func TestSlowGitHubDoesNotHoldDatabaseLocks(t *testing.T) {
	for _, operation := range []string{"propose", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			f, forge, m, owner, in := publicProposalFixture(t)
			seedPrivateGuard(t, f, m, owner)
			endpoint, payload := "/api/rules/doctrine/proposals", any(in)
			blockPath := "/git/trees"
			if operation == "refresh" {
				f.call(owner, "POST", endpoint, in, 200)
				endpoint += "/" + in.RequestID + "/refresh"
				payload, blockPath = nil, "/app"
			}
			entered, unblock := make(chan struct{}), make(chan struct{})
			defer close(unblock)
			forge.beforeRequest = func(r *http.Request) {
				if strings.HasSuffix(r.URL.Path, blockPath) {
					close(entered)
					select {
					case <-unblock:
					case <-r.Context().Done():
					}
				}
			}
			done := asyncProposal(f, owner, endpoint, payload)
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("request never reached slow GitHub")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			err := db.InTenant(ctx, f.d.App, owner.TenantID, func(tx pgx.Tx) error {
				// FK check on tenants + events append must complete while GitHub waits.
				_, err := events.Append(ctx, tx, owner, events.Change{Type: "test.unrelated_write", After: map[string]any{"ok": true}})
				if err != nil {
					return err
				}
				// Stronger than the FK regression: no tenant or proposal row lock remains.
				if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, owner.TenantID); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `SELECT id FROM doctrine_proposals WHERE id=$1 FOR UPDATE NOWAIT`, in.RequestID)
				return err
			})
			if err != nil {
				t.Fatalf("slow GitHub held a database lock: %v", err)
			}
			// A second worker cannot issue external operations for this proposal.
			f.call(owner, "POST", "/api/rules/doctrine/proposals/"+in.RequestID+"/refresh", nil, 409)
			unblock <- struct{}{}
			select {
			case w := <-done:
				if w.Code != 200 {
					t.Fatalf("request: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("request stuck after GitHub resumed")
			}
		})
	}
}

func TestProposalCASAndAuthorizationAfterIO(t *testing.T) {
	for _, change := range []string{"version", "authority"} {
		t.Run(change, func(t *testing.T) {
			f, forge, m, owner, in := publicProposalFixture(t)
			seedPrivateGuard(t, f, m, owner)
			f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
			forge.green()
			forge.beforeRequest = func(r *http.Request) {
				if r.URL.Path != "/app" {
					return
				}
				if change == "version" {
					_, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=jsonb_set(data,'{version}',to_jsonb((data->>'version')::bigint+1)) WHERE id=$1`, in.RequestID)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					_, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, owner.TenantID, owner.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			want := 409
			if change == "authority" {
				want = 403
			}
			f.call(owner, "POST", "/api/rules/doctrine/proposals/"+in.RequestID+"/refresh", nil, want)
			var state string
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT data->>'state' FROM doctrine_proposals WHERE id=$1`, in.RequestID).Scan(&state); err != nil || state != "proposed" {
				t.Fatalf("stale result overwrote state: %s %v", state, err)
			}
		})
	}
}

func TestExternalMergeAndNoOpRefresh(t *testing.T) {
	f, forge, m, owner, in := publicProposalFixture(t)
	seedPrivateGuard(t, f, m, owner)
	f.call(owner, "POST", "/api/rules/doctrine/proposals", in, 200)
	item := "/api/rules/doctrine/proposals/" + in.RequestID
	forge.green()
	f.call(owner, "POST", item+"/refresh", nil, 200)
	count := func() int {
		var n int
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, owner.TenantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	f.call(owner, "POST", item+"/refresh", nil, 200)
	if count() != before {
		t.Fatal("no-op refresh appended an event")
	}
	for key, pr := range forge.pulls {
		pr.Merged, pr.State, pr.MergeCommit = true, "closed", privateSHA
		forge.pulls[key] = pr
	}
	f.call(owner, "POST", item+"/approve", map[string]string{"head_sha": nextCommit}, 409)
	var p Proposal
	if err := json.Unmarshal(f.call(owner, "POST", item+"/refresh", nil, 200), &p); err != nil {
		t.Fatal(err)
	}
	if p.State != "merged" || p.ApprovedBy != "" || p.ReleaseRequested || forge.mergeCalls != 0 || forge.dispatches != 0 {
		t.Fatal("external merge attributed to observer or dispatched")
	}
	var n int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type IN ('doctrine.merged','doctrine.approved')`, owner.TenantID).Scan(&n); err != nil || n != 0 {
		t.Fatal("external merge audit falsely claims approval/merge")
	}
}

func TestTokenRevokedOnScopeErrorAndCancellation(t *testing.T) {
	f, forge, m, owner, _ := publicProposalFixture(t)
	_ = f
	forge.extraRepo = true
	if _, err := m.appClient(t.Context(), owner.TenantID, publicRepository); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if forge.minted != 1 || forge.revocations != 1 {
		t.Fatal("rejected installation token was not revoked")
	}
	forge.extraRepo = false
	ctx, cancel := context.WithCancel(t.Context())
	g, err := m.appClient(ctx, owner.TenantID, publicRepository)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	g.revoke()
	if forge.minted != forge.revocations || g.Token != "" {
		t.Fatal("cancelled operation retained token")
	}
}

func TestPrivateQuoteFailureIsGeneric(t *testing.T) {
	err := guardPrivateQuotes([]string{guardRule}, guardRule)
	var f *failure
	if !errors.As(err, &f) || f.Code != "private_doctrine" || strings.Contains(err.Error(), guardRule) {
		t.Fatal("private match reflected")
	}
}

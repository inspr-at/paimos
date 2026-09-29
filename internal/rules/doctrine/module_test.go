// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	privateRepo  = "inspr-at/fixture-doctrine-private"
	privateSHA   = "3333333333333333333333333333333333333333"
	fixtureToken = "ghp_fixtureTokenNeverStored318"
	privateProse = "PRIVATE_DOCTRINE_PROSE_318"
)

type doctrineFixture struct {
	t    *testing.T
	d    *dbtest.DB
	mux  *http.ServeMux
	fake *fakeGitHub
}

func (f doctrineFixture) tenant(slug string) string {
	var id string
	if err := f.d.App.QueryRow(f.t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f doctrineFixture) principal(tid, kind, name, role string, scopes []string, creator string) tenant.Principal {
	p := tenant.Principal{TenantID: tid, Kind: tenant.PrincipalKind(kind), Name: name, Scopes: scopes, KeyCreatorID: creator}
	err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, kind, name).Scan(&p.ID)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if role != "" {
		dbtest.BindRole(f.t, f.d, tid, p.ID, role)
	}
	return p
}

func (f doctrineFixture) call(p tenant.Principal, method, path string, in any, want int) []byte {
	f.t.Helper()
	body := ""
	if in != nil {
		raw, _ := json.Marshal(in)
		body = string(raw)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	if w.Code != want {
		f.t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	if strings.Contains(w.Body.String(), fixtureToken) {
		f.t.Fatalf("%s %s answered with the credential", method, path)
	}
	return w.Body.Bytes()
}

func (f doctrineFixture) layer(p tenant.Principal, method, path string, in any) Layer {
	f.t.Helper()
	var out Layer
	if err := json.Unmarshal(f.call(p, method, path, in, 200), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func find(l Layer, repo string) *SourceView {
	for i := range l.Sources {
		if l.Sources[i].Repository == repo {
			return &l.Sources[i]
		}
	}
	return nil
}

func TestDoctrineLayerEndToEnd(t *testing.T) {
	d := dbtest.Open(t)
	fake, srv := newFakeGitHub(t)
	fake.commit(fixtureRepo, fixtureCommit, fixtureFiles(), "v260922101217.0.0")
	next := fixtureFiles()
	next["docs/AGENTS-KERNEL.md"] = strings.Replace(fixtureKernel, "Small commits.", "Small, reviewed commits.", 1)
	fake.commit(fixtureRepo, nextCommit, next, "v260929100000.0.0")
	fake.commit(privateRepo, privateSHA, map[string]string{"docs/AGENTS-KERNEL-PRIVATE.md": "# Private kernel\n\n## Fleet\n\n- " + privateProse + "\n"}, "v1")
	fake.private[privateRepo] = true
	fake.token = fixtureToken
	creds := t.TempDir()
	if err := os.WriteFile(filepath.Join(creds, "doctrine-private-read"), []byte(fixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App, Options{CredentialsDir: creds, GitHubAPI: srv.URL, Client: srv.Client()}).Mount(mux)
	f := doctrineFixture{t: t, d: d, mux: mux, fake: fake}

	tid := f.tenant("doctrine-a")
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	member := f.principal(tid, "person", "member", "member", nil, "")
	viewer := f.principal(tid, "person", "viewer", "viewer", nil, "")
	reader := f.principal(tid, "agent", "reader", "member", []string{"rules.read"}, owner.ID)
	manager := f.principal(tid, "agent", "manager", "admin", []string{"rules.read", "settings.manage"}, owner.ID)

	empty := f.layer(member, "GET", "/api/rules/doctrine", nil)
	if len(empty.Sources) != 0 {
		t.Fatalf("%+v", empty)
	}
	public := map[string]any{"repository": fixtureRepo, "visibility": "public", "ref": "v260922101217.0.0"}
	f.call(member, "POST", "/api/rules/doctrine/sources", public, 403)
	f.call(reader, "POST", "/api/rules/doctrine/sources", public, 403)
	f.call(manager, "POST", "/api/rules/doctrine/sources", public, 403)
	f.call(viewer, "POST", "/api/rules/doctrine/sources", public, 403)
	if fake.calls != 0 {
		t.Fatal("an unauthorized write reached the repository host")
	}

	created := f.layer(owner, "POST", "/api/rules/doctrine/sources", public)
	src := find(created, fixtureRepo)
	if src == nil || src.State != "ready" || src.Commit != fixtureCommit || src.Ref != "v260922101217.0.0" || src.CommittedAt == nil || src.Error != "" {
		t.Fatalf("created %+v", src)
	}
	if len(src.Files) != 2 || src.Files[1].Path != "docs/AGENTS-KERNEL.md" || len(src.Files[1].Rules) != 4 || len(src.Skipped) != 1 {
		t.Fatalf("files %+v skipped %+v", src.Files, src.Skipped)
	}
	if rule := src.Files[1].Rules[0]; rule.Source != "<!-- aeon-rule: no-env-dump -->\r\n- 🔴 **NEVER** run `env`.\r\n  Why: it prints secrets.\r\n" || !strings.HasSuffix(rule.URL, "?plain=1#L7-L9") || rule.TLDR == nil {
		t.Fatalf("rule %+v", rule)
	}
	f.call(owner, "POST", "/api/rules/doctrine/sources", public, 409)

	// Readers see the same layer; an agent key with rules.read too.
	for _, p := range []tenant.Principal{member, reader} {
		got := f.layer(p, "GET", "/api/rules/doctrine", nil)
		if s := find(got, fixtureRepo); s == nil || s.State != "ready" || len(s.Files) != 2 {
			t.Fatalf("%s sees %+v", p.Name, got)
		}
	}

	// Private: a credential reference is required and resolved, never stored.
	private := map[string]any{"repository": privateRepo, "visibility": "private", "ref": "v1"}
	f.call(owner, "POST", "/api/rules/doctrine/sources", private, 400)
	private["credential_ref"] = "not-provisioned"
	if body := f.call(owner, "POST", "/api/rules/doctrine/sources", private, 422); !strings.Contains(string(body), "credential not-provisioned is not provisioned") {
		t.Fatalf("%s", body)
	}
	private["credential_ref"] = "doctrine-private-read"
	withPrivate := f.layer(owner, "POST", "/api/rules/doctrine/sources", private)
	ps := find(withPrivate, privateRepo)
	if ps == nil || ps.State != "ready" || len(ps.Files) != 1 || !strings.Contains(ps.Files[0].Rules[0].Source, privateProse) || ps.CredentialRef != "doctrine-private-read" {
		t.Fatalf("private %+v", ps)
	}
	if withPrivate.Sources[0].Visibility != "public" {
		t.Fatal("public doctrine is listed first")
	}
	sawToken := false
	for _, h := range fake.auth {
		sawToken = sawToken || h == "Bearer "+fixtureToken
	}
	if !sawToken {
		t.Fatal("the credential never reached the repository host")
	}

	// Moving the pin re-indexes and drops the old commit's bytes.
	f.call(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, map[string]any{"visibility": "public", "ref": "v260922101217.0.0", "commit": nextCommit}, 409)
	moved := find(f.layer(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, map[string]any{"visibility": "public", "ref": "v260929100000.0.0"}), fixtureRepo)
	if moved.Commit != nextCommit || moved.State != "ready" || !moved.PinnedAt.After(src.PinnedAt) || !strings.Contains(moved.Files[1].Rules[2].Source, "Small, reviewed commits.") {
		t.Fatalf("moved %+v", moved)
	}
	var stale int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_cache WHERE source_id=$1 AND commit_sha<>$2`, src.ID, nextCommit).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("old commit rows %d %v", stale, err)
	}
	// The database itself refuses bytes for a commit that is not pinned.
	err := db.InTenant(t.Context(), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id,source_id,commit_sha,path,blob_sha,content) VALUES($1,$2,$3,'x.md',$4,'x')`, tid, src.ID, fixtureCommit, strings.Repeat("a", 40))
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "pinned commit only") {
		t.Fatalf("stale insert err = %v", err)
	}

	// A failed re-index keeps the indexed files and says why.
	fake.down = true
	failed := find(f.layer(owner, "POST", "/api/rules/doctrine/sources/"+src.ID+"/index", nil), fixtureRepo)
	if failed.State != "ready" || len(failed.Files) != 2 || !strings.Contains(failed.Error, "GitHub answered 502") {
		t.Fatalf("failed %+v", failed)
	}
	f.call(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, map[string]any{"visibility": "public", "ref": "v260922101217.0.0"}, 422)
	fake.down = false
	if again := find(f.layer(owner, "POST", "/api/rules/doctrine/sources/"+src.ID+"/index", nil), fixtureRepo); again.Error != "" {
		t.Fatalf("retry %+v", again)
	}

	// Another workspace sees and changes nothing of this one.
	other := f.tenant("doctrine-b")
	outsider := f.principal(other, "person", "outsider", "owner", nil, "")
	if got := f.layer(outsider, "GET", "/api/rules/doctrine", nil); len(got.Sources) != 0 {
		t.Fatalf("tenant B sees %+v", got)
	}
	f.call(outsider, "PUT", "/api/rules/doctrine/sources/"+src.ID, map[string]any{"visibility": "public", "ref": "v260922101217.0.0"}, 404)
	f.call(outsider, "DELETE", "/api/rules/doctrine/sources/"+src.ID, nil, 404)
	f.call(member, "DELETE", "/api/rules/doctrine/sources/"+src.ID, nil, 403)
	f.call(owner, "PUT", "/api/rules/doctrine/sources/"+src.ID, map[string]any{"repository": privateRepo, "visibility": "public", "ref": "v1"}, 400)
	f.call(owner, "PUT", "/api/rules/doctrine/sources/NOT-A-UUID", map[string]any{"visibility": "public", "ref": "v1"}, 400)

	// No credential anywhere in the database; events hold configuration only.
	var leaked int
	if err := d.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM doctrine_sources s WHERE row_to_json(s)::text LIKE '%'||$1||'%') + (SELECT count(*) FROM events e WHERE e.before::text LIKE '%'||$1||'%' OR e.after::text LIKE '%'||$1||'%')`, fixtureToken).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("credential stored %d %v", leaked, err)
	}
	rows, err := d.Admin.Query(t.Context(), `SELECT type, coalesce(before::text,'') || coalesce(after::text,'') FROM events WHERE tenant_id=$1 AND type LIKE 'doctrine.%' ORDER BY id`, tid)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for rows.Next() {
		var kind, body string
		if err := rows.Scan(&kind, &body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, privateProse) || strings.Contains(body, "NEVER") {
			t.Fatalf("%s event holds doctrine text", kind)
		}
		types = append(types, kind)
	}
	if got := strings.Join(types, ","); got != "doctrine.source_added,doctrine.indexed,doctrine.source_added,doctrine.indexed,doctrine.pinned,doctrine.indexed,doctrine.indexed" {
		t.Fatalf("events %s", got)
	}

	// Removing a source removes its cached bytes.
	after := f.layer(owner, "DELETE", "/api/rules/doctrine/sources/"+ps.ID, nil)
	if find(after, privateRepo) != nil {
		t.Fatal("private source still listed")
	}
	var cached int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_cache WHERE source_id=$1`, ps.ID).Scan(&cached); err != nil || cached != 0 {
		t.Fatalf("cache after delete %d %v", cached, err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

const (
	credentialTenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	credentialTenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type readAppForge struct {
	reader                                                           *fakeGitHub
	minted, revoked, calls                                           int
	wrongRepo, extraPermission, wrongVisibility, missingInstallation bool
}

func readAppModule(t *testing.T) (*Module, *readAppForge) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	raw := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "app-key"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	reader, _ := newFakeGitHub(t)
	reader.commit(privateRepository, privateSHA, map[string]string{
		"docs/AGENTS-KERNEL.md": "# Kernel\n\n## Safety\n\n- Keep every final mutation authorized within its transaction.\n",
		"private-notes.txt":     "Invisible private wording remains covered by the quotation guard.",
	}, "main")
	reader.private[privateRepository], reader.token = true, fixtureToken
	forge := &readAppForge{reader: reader}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatal("App read left the fixed API origin")
		}
		forge.calls++
		w := httptest.NewRecorder()
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/app":
			send(map[string]any{"id": 8, "slug": "fixture-doctrine"})
		case "/app/installations/9/access_tokens":
			if forge.missingInstallation {
				w.WriteHeader(404)
				break
			}
			var in struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Repositories) != 1 || in.Repositories[0] != "inspr-doctrine-private" || len(in.Permissions) != 1 || in.Permissions["contents"] != "read" {
				t.Fatal("App read did not request exactly one repository and contents:read")
			}
			forge.minted++
			permissions := map[string]string{"contents": "read", "metadata": "read"}
			if forge.extraPermission {
				permissions["pull_requests"] = "read"
			}
			repository := privateRepository
			if forge.wrongRepo {
				repository = "someone/else"
			}
			send(map[string]any{"token": fixtureToken, "permissions": permissions, "repositories": []map[string]any{{"full_name": repository, "private": !forge.wrongVisibility}}})
		case "/installation/token":
			if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer "+fixtureToken || r.Context().Err() != nil {
				t.Fatal("token revocation lost its bounded independent context")
			}
			forge.revoked++
			w.WriteHeader(204)
		default:
			reader.ServeHTTP(w, r)
		}
		return w.Result(), nil
	})}
	m := New(nil, Options{CredentialsDir: dir, Client: client, GuardKey: testGuardMaster(), DefaultSource: true, App: AppConfig{ID: "8", InstallationID: "9", KeyRef: "app-key", TenantID: credentialTenantA}})
	allowCredential(t, dir, "app-key", credentialTenantA, privateRepository)
	return m, forge
}

// Risk: a read could acquire broad App authority, leak a token, or bypass a
// tenant grant. Exercise the whole fetch and private guard without a token file.
func TestDoctrineAppReadBoundary(t *testing.T) {
	for _, scenario := range []string{"read", "wrong repository", "extra permission", "wrong visibility", "missing installation", "tenant denied", "grant revoked", "fetch failed", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := readAppModule(t)
			tid := credentialTenantA
			s := Source{Repository: privateRepository, Visibility: "private", Commit: privateSHA, Paths: DefaultPaths, CredentialRef: "github-app"}
			ctx := t.Context()
			switch scenario {
			case "wrong repository":
				f.wrongRepo = true
			case "extra permission":
				f.extraPermission = true
			case "wrong visibility":
				f.wrongVisibility = true
			case "missing installation":
				f.missingInstallation = true
			case "tenant denied":
				tid = credentialTenantB
			case "grant revoked":
				allowCredential(t, m.credentials.Dir, "app-key", credentialTenantB, privateRepository)
			case "fetch failed":
				f.reader.down = true
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			files, _, corpus, err := m.fetch(ctx, tid, s)
			if scenario == "read" {
				if err != nil || len(files) != 1 || len(corpus) == 0 {
					t.Fatalf("App fetch: files=%d corpus=%d err=%v", len(files), len(corpus), err)
				}
				guard, err := unmarshalGuard(corpus, m.guardKey(tid))
				if err != nil || guardPrivateQuotes(guard, nil, "Invisible private wording remains covered by the quotation guard.") == nil {
					t.Fatal("App guard missed text outside the selected paths")
				}
			} else if err == nil {
				t.Fatal("App boundary did not fail closed")
			}
			expected := map[string]string{
				"wrong repository": "unapproved repository", "extra permission": "unexpected permissions",
				"wrong visibility": "visibility", "missing installation": "GitHub did not accept",
				"tenant denied": "credential unavailable", "grant revoked": "credential unavailable",
				"fetch failed": "502", "cancelled": "GitHub did not confirm",
			}
			if message := expected[scenario]; message != "" && !strings.Contains(err.Error(), message) {
				t.Fatalf("wrong failure: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), fixtureToken) || strings.Contains(logs.String(), fixtureToken) {
				t.Fatal("App token escaped through an error or log")
			}
			if f.revoked != f.minted {
				t.Fatalf("minted=%d revoked=%d", f.minted, f.revoked)
			}
			if (scenario == "tenant denied" || scenario == "grant revoked") && f.calls != 0 {
				t.Fatal("a refused grant reached GitHub")
			}
			if _, err := os.Stat(filepath.Join(m.credentials.Dir, "github-app")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("App mode created a static token file")
			}
		})
	}
}

func mirrorFixture(t *testing.T, commit string) string {
	t.Helper()
	dir := t.TempDir()
	tree := filepath.Join(dir, privateRepository)
	if err := os.MkdirAll(filepath.Join(tree, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, raw := range map[string]string{
		mirrorMarker:            commit + "\n",
		"docs/AGENTS-KERNEL.md": "# Kernel\n\n## Safety\n\n- Authorize every final mutation inside its transaction.\n",
		"private-notes.txt":     "These unpublished private notes remain guarded outside the visible index.",
	} {
		if err := os.WriteFile(filepath.Join(tree, path), []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
	}
	allowCredential(t, dir, "host-mirror", credentialTenantA, privateRepository)
	return dir
}

// The positive fixtures model the host's read-only mount at the syscall seam;
// the writable-mount case below uses the real statfs check. Confinement, marker,
// byte limits, full-tree guard and the denied network transport stay real.
func simulatedReadOnly(*os.File) bool { return true }

// SimulateReadOnlyMirrorForTest exposes only the statfs seam to external
// integration tests. Marker validation and tenant authorization remain real.
func SimulateReadOnlyMirrorForTest(m *Module) {
	m.credentials.mirrorReadOnly = simulatedReadOnly
}

func TestDoctrineHostMirrorBoundary(t *testing.T) {
	for _, scenario := range []string{"read", "missing marker", "garbled marker", "symlink escape", "marker symlink", "writable mount", "tenant denied", "wrong repository", "oversized tree", "stale pin"} {
		t.Run(scenario, func(t *testing.T) {
			dir := mirrorFixture(t, privateSHA)
			m := New(nil, Options{MirrorDir: dir, GuardKey: testGuardMaster(), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("host mirror attempted network access")
				return nil, errors.New("network blocked")
			})}})
			m.credentials.mirrorReadOnly = simulatedReadOnly
			s := Source{Repository: privateRepository, Visibility: "private", Commit: privateSHA, Paths: DefaultPaths, CredentialRef: "host-mirror"}
			tid := credentialTenantA
			tree := filepath.Join(dir, privateRepository)
			switch scenario {
			case "missing marker":
				if err := os.Rename(filepath.Join(tree, mirrorMarker), filepath.Join(tree, "marker-hidden")); err != nil {
					t.Fatal(err)
				}
			case "garbled marker":
				if err := os.WriteFile(filepath.Join(tree, mirrorMarker), []byte("not-a-commit"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink escape":
				if err := os.Symlink(t.TempDir(), filepath.Join(tree, "escape")); err != nil {
					t.Fatal(err)
				}
			case "marker symlink":
				marker := filepath.Join(tree, mirrorMarker)
				if err := os.Rename(marker, filepath.Join(t.TempDir(), "marker")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/outside/marker", marker); err != nil {
					t.Fatal(err)
				}
			case "writable mount":
				m.credentials.mirrorReadOnly = nil
			case "tenant denied":
				tid = credentialTenantB
			case "wrong repository":
				s.Repository = "someone/else"
			case "oversized tree":
				if err := os.WriteFile(filepath.Join(tree, "too-large.txt"), bytes.Repeat([]byte("x"), maxGuardFile+1), 0644); err != nil {
					t.Fatal(err)
				}
			case "stale pin":
				s.Commit = nextCommit
			}
			files, _, corpus, err := m.fetch(t.Context(), tid, s)
			if scenario != "read" {
				if err == nil {
					t.Fatal("mirror boundary did not fail closed")
				}
				expected := map[string]string{
					"missing marker": "unavailable", "garbled marker": "full commit marker", "symlink escape": "symlinks",
					"marker symlink": "symlinks", "writable mount": "mounted read-only", "tenant denied": "credential unavailable",
					"wrong repository": "credential unavailable", "oversized tree": "size bounds", "stale pin": "pinned commit",
				}
				if !strings.Contains(err.Error(), expected[scenario]) {
					t.Fatalf("wrong failure: %v", err)
				}
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("mirror fetch: files=%d err=%v", len(files), err)
			}
			guard, err := unmarshalGuard(corpus, m.guardKey(tid))
			if err != nil || guardPrivateQuotes(guard, nil, "These unpublished private notes remain guarded outside the visible index.") == nil {
				t.Fatal("mirror guard missed unselected text")
			}
		})
	}
}

// Risk: background registration could overwrite a person's source, duplicate
// audit events on restart, or index the wrong marker when the host refreshes.
func TestDoctrineHostDefaultRegistration(t *testing.T) {
	for _, mode := range []string{"github-app", "host-mirror"} {
		t.Run(mode, func(t *testing.T) {
			d := dbtest.Open(t)
			m, forge := readAppModule(t)
			m.pool = d.App
			mux := http.NewServeMux()
			m.Mount(mux)
			f := doctrineFixture{t: t, d: d, mux: mux, fake: forge.reader}
			tid := f.tenant("default-doctrine-" + mode)
			m.app.TenantID = tid
			allowCredential(t, m.credentials.Dir, "app-key", tid, privateRepository)
			if mode == "host-mirror" {
				m.credentials.MirrorDir = mirrorFixture(t, privateSHA)
				m.credentials.mirrorReadOnly = simulatedReadOnly
				allowCredential(t, m.credentials.MirrorDir, "host-mirror", tid, privateRepository)
				m.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("mirror registration reached network")
					return nil, errors.New("blocked")
				})}
			}
			owner := f.principal(tid, "person", "owner", "admin", nil, "")
			if err := m.EnsureDefaultSource(t.Context()); err != nil {
				t.Fatal(err)
			}
			source := find(f.layer(owner, "GET", "/api/rules/doctrine", nil), privateRepository)
			if source == nil || source.State != "ready" || source.Commit != privateSHA || source.CredentialRef != mode {
				t.Fatalf("default source: %+v", source)
			}
			// Two new module instances model process restarts; no new source event.
			for i := 0; i < 2; i++ {
				restarted := New(d.App, Options{CredentialsDir: m.credentials.Dir, MirrorDir: m.credentials.MirrorDir, Client: m.client, GuardKey: m.guardMaster, DefaultSource: true, App: m.app})
				restarted.credentials.mirrorReadOnly = m.credentials.mirrorReadOnly
				if err := restarted.EnsureDefaultSource(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			var adds int
			if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='doctrine.source_added'`, tid).Scan(&adds); err != nil || adds != 1 {
				t.Fatalf("source_added=%d err=%v", adds, err)
			}
			agent := f.principal(tid, "agent", "builder", "admin", []string{"settings.manage", "rules.read"}, owner.ID)
			f.call(agent, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: "someone/else", Visibility: "private", Ref: "main", CredentialRef: mode}, 403)
			if mode == "host-mirror" {
				marker := filepath.Join(m.credentials.MirrorDir, privateRepository, mirrorMarker)
				if err := os.WriteFile(marker, []byte(nextCommit+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
				stale := find(f.layer(owner, "GET", "/api/rules/doctrine", nil), privateRepository)
				if stale.State != "failed" || len(stale.Files) != 0 {
					t.Fatal("changed marker exposed stale cached rules")
				}
				if err := db.InTenant(t.Context(), d.App, tid, func(tx pgx.Tx) error {
					_, err := m.privateGuard(t.Context(), tx, owner)
					if err == nil {
						t.Fatal("changed marker retained a stale private guard")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				refreshed := find(f.layer(owner, "POST", "/api/rules/doctrine/sources/"+source.ID+"/index", nil), privateRepository)
				if refreshed.Commit != nextCommit || refreshed.State != "ready" {
					t.Fatalf("reindexed mirror: %+v", refreshed)
				}
				allowCredential(t, m.credentials.MirrorDir, "host-mirror", credentialTenantB, privateRepository)
			} else {
				if forge.minted != forge.revoked {
					t.Fatal("registration leaked a minted token")
				}
				allowCredential(t, m.credentials.Dir, "app-key", credentialTenantB, privateRepository)
			}
			denied := find(f.layer(owner, "GET", "/api/rules/doctrine", nil), privateRepository)
			if denied.State != "failed" || len(denied.Files) != 0 {
				t.Fatal("revoked host grant exposed cached rules")
			}
		})
	}
}

func TestDoctrineHostDefaultPreservesTokenFileSource(t *testing.T) {
	d := dbtest.Open(t)
	m, forge := readAppModule(t)
	m.pool = d.App
	mux := http.NewServeMux()
	m.Mount(mux)
	f := doctrineFixture{t: t, d: d, mux: mux, fake: forge.reader}
	tid := f.tenant("default-legacy-token")
	m.app.TenantID = tid
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	const ref = "existing-read"
	if err := os.WriteFile(filepath.Join(m.credentials.Dir, ref), []byte(fixtureToken), 0600); err != nil {
		t.Fatal(err)
	}
	allowCredential(t, m.credentials.Dir, ref, tid, privateRepository)
	paths := []string{"docs/AGENTS-KERNEL.md"}
	initial := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: privateRepository, Visibility: "private", Commit: privateSHA, Paths: paths, CredentialRef: ref}), privateRepository)
	// The default App has no grant for this tenant: keeping a person's source
	// must not require it or mint an App token.
	if err := m.EnsureDefaultSource(t.Context()); err != nil {
		t.Fatal(err)
	}
	kept := find(f.layer(owner, "GET", "/api/rules/doctrine", nil), privateRepository)
	if initial == nil || kept == nil || kept.ID != initial.ID || kept.Commit != privateSHA || kept.CredentialRef != ref || len(kept.Paths) != 1 || kept.Paths[0] != paths[0] || kept.State != "ready" || forge.minted != 0 {
		t.Fatal("host default altered a tenant's token-file source")
	}
}

// Risk: routine host exports could take down every catalog consumer, even
// though the indexed pin remains authorized. A stale guard must still refuse
// public proposals, and actual grant or mirror failures must hide the cache.
func TestDoctrineHostMirrorPinnedCatalogSurvivesMarkerAdvance(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, Options{MirrorDir: mirrorFixture(t, privateSHA), GuardKey: testGuardMaster(), DefaultSource: true})
	m.credentials.mirrorReadOnly = simulatedReadOnly
	mux := http.NewServeMux()
	m.Mount(mux)
	f := doctrineFixture{t: t, d: d, mux: mux}
	tid := f.tenant("mirror-pinned-delivery")
	m.app.TenantID = tid
	allowCredential(t, m.credentials.MirrorDir, "host-mirror", tid, privateRepository)
	owner := f.principal(tid, "person", "owner", "admin", nil, "")
	if err := m.EnsureDefaultSource(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), catalogCredentialsKey{}, m.credentials)
	catalog := func() (Catalog, error) {
		t.Helper()
		var cat Catalog
		err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			var err error
			cat, err = LoadCatalog(ctx, tx)
			return err
		})
		return cat, err
	}
	initial, err := catalog()
	if err != nil || len(initial.Releases) != 1 || len(initial.Rules) != 1 || initial.Releases[0].Commit != privateSHA {
		t.Fatalf("initial catalog: %+v err=%v", initial, err)
	}
	tree := filepath.Join(m.credentials.MirrorDir, privateRepository)
	if err := os.WriteFile(filepath.Join(tree, "docs/AGENTS-KERNEL.md"), []byte("# Kernel\n\n## Safety\n\n- A rule only available at the new host commit.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, mirrorMarker), []byte(nextCommit+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stale, err := catalog()
	if err != nil {
		t.Fatalf("marker advance interrupted pinned catalog delivery: %v", err)
	}
	if len(stale.Releases) != 1 || stale.Releases[0].Commit != privateSHA || !slices.Equal(stale.Rules, initial.Rules) {
		t.Fatalf("marker advance changed the authorized indexed pin: %+v", stale)
	}
	if pointer := stale.Pointer(); !strings.Contains(pointer, privateSHA) || !strings.Contains(pointer, "stale") || strings.Contains(pointer, nextCommit) {
		t.Fatalf("delivery did not mark the pinned release stale: %q", pointer)
	}
	visible := find(f.layer(owner, "GET", "/api/rules/doctrine", nil), privateRepository)
	if visible == nil || visible.State != "ready" || visible.Commit != privateSHA || len(visible.Files) != 1 || len(visible.Files[0].Rules) != 1 || !strings.Contains(visible.Error, "stale") || visible.Files[0].Rules[0].Text != initial.Rules[0].Text {
		t.Fatalf("visible index lost or mislabeled the stale pin: %+v", visible)
	}
	if err := db.InTenant(t.Context(), d.App, tid, func(tx pgx.Tx) error {
		guard, err := m.privateGuard(t.Context(), tx, owner)
		var failure *failure
		if guard != nil || !errors.As(err, &failure) || failure.Code != "private_index_unavailable" {
			t.Fatalf("stale private guard did not fail closed: guard=%v err=%v", guard, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"revoked grant", "invalid marker", "writable mount"} {
		t.Run(denied, func(t *testing.T) {
			allowCredential(t, m.credentials.MirrorDir, "host-mirror", tid, privateRepository)
			if err := os.WriteFile(filepath.Join(tree, mirrorMarker), []byte(nextCommit+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			m.credentials.mirrorReadOnly = simulatedReadOnly
			switch denied {
			case "revoked grant":
				allowCredential(t, m.credentials.MirrorDir, "host-mirror", credentialTenantB, privateRepository)
			case "invalid marker":
				if err := os.WriteFile(filepath.Join(tree, mirrorMarker), []byte("invalid\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "writable mount":
				m.credentials.mirrorReadOnly = func(*os.File) bool { return false }
			}
			ctx = context.WithValue(t.Context(), catalogCredentialsKey{}, m.credentials)
			cat, err := catalog()
			if err == nil || len(cat.Releases) != 0 || len(cat.Rules) != 0 {
				t.Fatalf("%s exposed cached doctrine: %+v err=%v", denied, cat, err)
			}
		})
	}
}

// Risk: reindexing a person's public source could perform host-owned work for
// another tenant, or fail and disclose host state when default setup is broken.
func TestDoctrineReindexIsolatesHostDefaultFailures(t *testing.T) {
	for _, scenario := range []string{"other tenant", "host grant missing", "host installation unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			d := dbtest.Open(t)
			m, forge := readAppModule(t)
			m.pool = d.App
			forge.reader.commit(fixtureRepo, fixtureCommit, fixtureFiles(), "main")
			mux := http.NewServeMux()
			m.Mount(mux)
			f := doctrineFixture{t: t, d: d, mux: mux, fake: forge.reader}
			hostTenant := f.tenant("reindex-host")
			m.app.TenantID = hostTenant
			tid := hostTenant
			if scenario == "other tenant" {
				tid = f.tenant("reindex-other")
			}
			allowCredential(t, m.credentials.Dir, "app-key", hostTenant, privateRepository)
			owner := f.principal(tid, "person", "owner", "admin", nil, "")
			source := find(f.layer(owner, "POST", "/api/rules/doctrine/sources", SourceInput{Repository: fixtureRepo, Visibility: "public", Ref: "main"}), fixtureRepo)
			if source == nil || source.State != "ready" {
				t.Fatalf("public fixture not indexed: %+v", source)
			}
			switch scenario {
			case "host grant missing":
				allowCredential(t, m.credentials.Dir, "app-key", credentialTenantB, privateRepository)
			case "host installation unavailable":
				forge.missingInstallation = true
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			refreshed := find(f.layer(owner, "POST", "/api/rules/doctrine/sources/"+source.ID+"/index", nil), fixtureRepo)
			if refreshed == nil || refreshed.State != "ready" || refreshed.Error != "" || refreshed.Commit != fixtureCommit || len(refreshed.Files) != len(source.Files) {
				t.Fatalf("host setup broke public-source reindex: %+v", refreshed)
			}
			var hostSources int
			if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM doctrine_sources WHERE tenant_id=$1 AND repository=$2`, hostTenant, privateRepository).Scan(&hostSources); err != nil || hostSources != 0 {
				t.Fatalf("person's reindex registered a host source: count=%d err=%v", hostSources, err)
			}
			if forge.minted != 0 || forge.revoked != 0 {
				t.Fatal("person's reindex minted host-tenant tokens")
			}
			if scenario == "other tenant" && strings.Contains(logs.String(), "doctrine default source") {
				t.Fatal("another tenant's reindex attempted host-default maintenance")
			}
			if scenario != "other tenant" && !strings.Contains(logs.String(), "doctrine default source") {
				t.Fatal("host-default setup failure was not logged")
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

func person(t *testing.T, d *dbtest.DB, slug, email string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{Kind: tenant.Person, Roles: []string{"member"}}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,email,display_name) VALUES('test',$1,$2,'Person') RETURNING id::text`, slug, email).Scan(&identity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,identity_id,name) VALUES($1,'person',$2,'Person') RETURNING id::text`, p.TenantID, identity).Scan(&p.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, p.ID, "member")
	return p
}
func mux(d *dbtest.DB, store attachments.Store) *http.ServeMux {
	m := http.NewServeMux()
	New(d.App, store).Mount(m)
	events.New(d.App, events.WithUndoHandlers(UndoHandlers())).Mount(m)
	return m
}
func req(t *testing.T, h http.Handler, p tenant.Principal, method, path, ct string, body *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, body)
	}
	r = r.WithContext(tenant.WithPrincipal(t.Context(), p))
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func fixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 255), uint8(y % 255), 50, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func avatarBody(t *testing.T, source []byte) (string, *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	f, err := w.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(source); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("crop", `{"x":50,"y":0,"size":300}`); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return w.FormDataContentType(), &b
}
func decodeProfile(t *testing.T, w *httptest.ResponseRecorder) Profile {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var p Profile
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestValidateAndCrop(t *testing.T) {
	_, errs := ValidatePatch([]byte(`{"short_name":"MBA","timezone":"Mars/Base","locale":"not a locale!","email":"x","greeting_enabled":"yes"}`))
	for _, key := range []string{"short_name", "timezone", "locale", "email", "greeting_enabled"} {
		if errs[key] == "" {
			t.Errorf("missing %s error", key)
		}
	}
	orig, variants, err := ProcessAvatar(fixture(t), Crop{X: 50, Y: 0, Size: 300})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(orig))
	if err != nil || cfg.Width != 400 || cfg.Height != 300 {
		t.Fatalf("original %v %v", cfg, err)
	}
	for _, n := range []int{32, 64, 128, 256} {
		cfg, err := png.DecodeConfig(bytes.NewReader(variants[n]))
		if err != nil || cfg.Width != n || cfg.Height != n {
			t.Errorf("variant %d: %v %v", n, cfg, err)
		}
	}
	if _, _, err := ProcessAvatar(fixture(t), Crop{X: 200, Y: 0, Size: 300}); err == nil {
		t.Fatal("accepted out of bounds crop")
	}
}
func TestJPEGEXIFOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	// EXIF orientation 6 rotates the decoded 4x2 JPEG to 2x4.
	exif := []byte{0xff, 0xe1, 0x00, 0x22, 'E', 'x', 'i', 'f', 0, 0,
		'I', 'I', 0x2a, 0, 8, 0, 0, 0, 1, 0,
		0x12, 0x01, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	data := append(append([]byte{}, encoded.Bytes()[:2]...), exif...)
	data = append(data, encoded.Bytes()[2:]...)
	if got := jpegOrientation(data); got != 6 {
		t.Fatalf("orientation %d", got)
	}
	original, _, err := ProcessAvatar(data, Crop{X: 0, Y: 1, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(original))
	if err != nil || cfg.Width != 2 || cfg.Height != 4 {
		t.Fatalf("oriented dimensions %v %v", cfg, err)
	}
}
func TestProfileTenantUniquenessAvatarAndUndo(t *testing.T) {
	d := dbtest.Open(t)
	a := person(t, d, "profile_a", "a@example.test")
	b := person(t, d, "profile_b", "b@example.test")
	var other tenant.Principal
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other') RETURNING id::text`, a.TenantID).Scan(&other.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	other.TenantID = a.TenantID
	other.Kind = tenant.Person
	store := attachments.Store{FilesDir: t.TempDir()}
	h := mux(d, store)
	patch := func(p tenant.Principal, s string) *httptest.ResponseRecorder {
		return req(t, h, p, "PATCH", "/api/me/profile", "application/json", bytes.NewBufferString(s))
	}
	first := decodeProfile(t, patch(a, `{"first_name":"Anna","last_name":"Barta","short_name":"mba","timezone":"Europe/Vienna","locale":"de-AT"}`))
	if first.Initials != "AB" || first.WeekStart != 1 || first.Email == nil || *first.Email != "a@example.test" {
		t.Fatalf("profile %+v", first)
	}
	if got := patch(other, `{"short_name":"mba"}`); got.Code != 409 {
		t.Fatalf("duplicate HTTP %d", got.Code)
	}
	if got := patch(b, `{"short_name":"mba"}`); got.Code != 200 {
		t.Fatalf("cross tenant duplicate HTTP %d", got.Code)
	}
	ct, body := avatarBody(t, fixture(t))
	withAvatar := decodeProfile(t, req(t, h, a, "POST", "/api/me/avatar", ct, body))
	if len(withAvatar.AvatarHashes) != 4 {
		t.Fatalf("hashes %+v", withAvatar.AvatarHashes)
	}
	// Current avatar bytes, including independently addressed size variants,
	// survive cleanup at an age that would qualify a true orphan.
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := filepath.WalkDir(store.FilesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatal(err)
	}
	assertAvatarInventory := func() {
		t.Helper()
		gc, err := attachments.GC(t.Context(), d.App, store, a.TenantID, true)
		if err != nil || gc.Candidates != 0 || gc.Removed != 0 {
			t.Fatalf("avatar GC %+v: %v", gc, err)
		}
		verified, err := attachments.Verify(t.Context(), d.App, store, a.TenantID)
		if err != nil || verified.Checked < 4 || len(verified.Issues) != 0 {
			t.Fatalf("avatar inventory %+v: %v", verified, err)
		}
	}
	assertAvatarInventory()
	hash := withAvatar.AvatarHashes["64"]
	path := "/api/people/" + a.ID + "/avatar/64?v=" + hash
	got := req(t, h, b, "GET", path, "", nil)
	if got.Code != 404 {
		t.Fatalf("tenant leak HTTP %d", got.Code)
	}
	got = req(t, h, a, "GET", path, "", nil)
	if got.Code != 200 || got.Header().Get("ETag") != `"`+hash+`"` || !strings.Contains(got.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("avatar HTTP %d headers %v", got.Code, got.Header())
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(got.Body.Bytes()))
	if err != nil || cfg.Width != 64 {
		t.Fatalf("avatar dimensions %v %v", cfg, err)
	}
	r := httptest.NewRequest("GET", path, nil).WithContext(tenant.WithPrincipal(t.Context(), a))
	r.Header.Set("If-None-Match", `"`+hash+`"`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Fatalf("etag HTTP %d", w.Code)
	}
	deleted := decodeProfile(t, req(t, h, a, "DELETE", "/api/me/avatar", "", nil))
	if len(deleted.AvatarHashes) != 0 || deleted.Initials != "AB" {
		t.Fatalf("delete %+v", deleted)
	}
	// The avatar is now referenced only by history; undo must still work.
	assertAvatarInventory()
	var eventID int64
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE tenant_id=$1 AND type='profile.updated' AND after->>'avatar_original_hash'='' ORDER BY id DESC LIMIT 1`, a.TenantID).Scan(&eventID)
	})
	if err != nil {
		t.Fatal(err)
	}
	undo := req(t, h, a, "POST", "/api/events/"+fmt.Sprint(eventID)+"/undo", "", nil)
	if undo.Code != 201 {
		t.Fatalf("undo HTTP %d: %s", undo.Code, undo.Body.String())
	}
	restored := decodeProfile(t, req(t, h, a, "GET", "/api/me/profile", "", nil))
	if restored.AvatarHashes["64"] != hash {
		t.Fatalf("undo avatar %+v", restored)
	}
}
func TestClassicProfileImportIdempotent(t *testing.T) {
	d := dbtest.Open(t)
	p := person(t, d, "profile_import", "import@example.test")
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Classic Paimos importer')`, p.TenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	img := fixture(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Errorf("classic mutation %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth")
		}
		switch r.URL.Path {
		case "/api/users":
			_, _ = w.Write([]byte(`[{"email":"import@example.test","first_name":"Markus","last_name":"Barta","nickname":"Mark","username":"mba","timezone":"Europe/Vienna","locale":"de-AT","avatar_path":"/static/avatars/mba.png"}]`))
		case "/static/avatars/mba.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(img)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(key, []byte("test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewClassicSource(server.URL, key, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	job := ProfileImporter{Pool: d.App, Store: attachments.Store{FilesDir: t.TempDir()}, Source: source}
	report, err := job.Run(t.Context(), "profile_import", false)
	if err != nil || report.WouldWrite != 1 || report.Written != 0 {
		t.Fatalf("dry run %+v %v", report, err)
	}
	report, err = job.Run(t.Context(), "profile_import", true)
	if err != nil || report.Written != 1 {
		t.Fatalf("apply %+v %v", report, err)
	}
	report, err = job.Run(t.Context(), "profile_import", true)
	if err != nil || report.Written != 0 {
		t.Fatalf("replay %+v %v", report, err)
	}
	if calls < 4 {
		t.Fatalf("source calls %d", calls)
	}
	got := decodeProfile(t, req(t, mux(d, job.Store), p, "GET", "/api/me/profile", "", nil))
	if got.PreferredName != "Mark" || got.ShortName != "mba" || len(got.AvatarHashes) != 4 {
		t.Fatalf("imported %+v", got)
	}
	var count int
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='profile.updated'`, p.TenantID).Scan(&count)
	})
	if err != nil || count != 1 {
		t.Fatalf("events %d %v", count, err)
	}
}

func TestClassicProfileImportRequiresASCIIMailboxMatch(t *testing.T) {
	for _, tc := range []struct {
		stored, imported string
		match            bool
	}{
		{"admin@example.test", "admİn@example.test", false},
		{"mark@example.test", "marK@example.test", false},
		{"sam@example.test", "ſam@example.test", false},
		{"mark@example.test", "MARK@EXAMPLE.TEST", true},
		{"admİn@example.test", "ADMİN@EXAMPLE.TEST", true},
	} {
		t.Run(tc.imported, func(t *testing.T) {
			d := dbtest.Open(t)
			p := person(t, d, "profile_email", tc.stored)
			if _, err := d.Admin.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Classic Paimos importer')`, p.TenantID); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/users" {
					t.Errorf("unexpected classic request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode([]map[string]string{{"email": tc.imported, "first_name": "Imported"}})
			}))
			defer server.Close()
			base, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			job := ProfileImporter{Pool: d.App, Source: &ClassicSource{base: base, client: server.Client()}}
			for _, apply := range []bool{false, true} {
				report, err := job.Run(t.Context(), "profile_email", apply)
				if err != nil {
					t.Fatal(err)
				}
				if tc.match {
					if report.Matched != 1 || (!apply && report.WouldWrite != 1) || (apply && report.Written != 1) {
						t.Fatalf("matching profile not imported (apply=%v): %+v", apply, report)
					}
				} else if report.Matched != 0 || report.Skipped != 1 || report.Written != 0 || report.WouldWrite != 0 {
					t.Fatalf("different mailbox matched (apply=%v): %+v", apply, report)
				}
			}
			got := decodeProfile(t, req(t, mux(d, attachments.Store{}), p, "GET", "/api/me/profile", "", nil))
			var count int
			if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='profile.updated'`, p.TenantID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if tc.match {
				if got.FirstName != "Imported" || count != 1 {
					t.Fatalf("matching profile not written/audited: first=%s events=%d", got.FirstName, count)
				}
			} else if got.FirstName != "" || count != 0 {
				t.Fatalf("different person's profile overwritten: first=%s events=%d", got.FirstName, count)
			}
		})
	}
}

// The default pace (AEON-1175) feeds every plan read: a stored value outside
// 1–50 would make the person's plan unreadable, so the patch refuses it whole,
// null follows the built-in default, and undo restores the value before.
func TestDefaultPaceIsBoundedAndUndoable(t *testing.T) {
	d := dbtest.Open(t)
	a := person(t, d, "profile_pace", "pace@example.test")
	h := mux(d, attachments.Store{FilesDir: t.TempDir()})
	patch := func(s string) *httptest.ResponseRecorder {
		return req(t, h, a, "PATCH", "/api/me/profile", "application/json", bytes.NewBufferString(s))
	}
	planPoints := func() int {
		t.Helper()
		var points int
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
			var err error
			_, points, _, _, err = agentaccounts.DailyContextTx(t.Context(), tx, a.ID, time.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return points
	}
	if got := decodeProfile(t, req(t, h, a, "GET", "/api/me/profile", "", nil)); got.DailyPointsPerDay != nil || planPoints() != 10 {
		t.Fatalf("fresh profile pace %v, plan %d", got.DailyPointsPerDay, planPoints())
	}
	set := decodeProfile(t, patch(`{"daily_points_per_day":14}`))
	if set.DailyPointsPerDay == nil || *set.DailyPointsPerDay != 14 || planPoints() != 14 {
		t.Fatalf("set pace %v, plan %d", set.DailyPointsPerDay, planPoints())
	}
	for _, body := range []string{`{"daily_points_per_day":0}`, `{"daily_points_per_day":51}`, `{"daily_points_per_day":-1}`, `{"daily_points_per_day":10.5}`, `{"daily_points_per_day":1e1}`, `{"daily_points_per_day":"12"}`, `{"daily_points_per_day":true}`, `{"first_name":"Refused","daily_points_per_day":0}`} {
		got := patch(body)
		var out struct{ Errors map[string]string }
		if got.Code != 400 || json.Unmarshal(got.Body.Bytes(), &out) != nil || out.Errors["daily_points_per_day"] == "" {
			t.Fatalf("%s: HTTP %d %s", body, got.Code, got.Body.String())
		}
	}
	if kept := decodeProfile(t, req(t, h, a, "GET", "/api/me/profile", "", nil)); kept.FirstName != "" || kept.DailyPointsPerDay == nil || *kept.DailyPointsPerDay != 14 {
		t.Fatalf("a refused patch changed the profile: %+v", kept)
	}
	if cleared := decodeProfile(t, patch(`{"daily_points_per_day":null}`)); cleared.DailyPointsPerDay != nil || planPoints() != 10 {
		t.Fatalf("null pace %v, plan %d", cleared.DailyPointsPerDay, planPoints())
	}
	var eventID int64
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM events WHERE tenant_id=$1 AND type='profile.updated' AND before->>'daily_points_per_day'='14' AND after->'daily_points_per_day'='null'::jsonb ORDER BY id DESC LIMIT 1`, a.TenantID).Scan(&eventID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if undo := req(t, h, a, "POST", "/api/events/"+fmt.Sprint(eventID)+"/undo", "", nil); undo.Code != 201 {
		t.Fatalf("undo HTTP %d: %s", undo.Code, undo.Body.String())
	}
	if restored := decodeProfile(t, req(t, h, a, "GET", "/api/me/profile", "", nil)); restored.DailyPointsPerDay == nil || *restored.DailyPointsPerDay != 14 || planPoints() != 14 {
		t.Fatalf("undo pace %v, plan %d", restored.DailyPointsPerDay, planPoints())
	}
}

// Undo of the default pace (AEON-1175) is guarded by the revision its own edit
// left. Risk: Undo overwrites a change made since in another tab. A patch with
// expected_revision applies only to the profile at that revision; otherwise it
// is refused with 409 and nothing is written, and a malformed guard is refused.
func TestProfilePatchGuardedByExpectedRevision(t *testing.T) {
	d := dbtest.Open(t)
	a := person(t, d, "profile_guard", "guard@example.test")
	h := mux(d, attachments.Store{FilesDir: t.TempDir()})
	patch := func(s string) *httptest.ResponseRecorder {
		return req(t, h, a, "PATCH", "/api/me/profile", "application/json", bytes.NewBufferString(s))
	}
	updates := func() int {
		t.Helper()
		var n int
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='profile.updated'`, a.TenantID).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	edit := decodeProfile(t, patch(`{"daily_points_per_day":30,"expected_revision":0}`))
	if edit.DailyPointsPerDay == nil || *edit.DailyPointsPerDay != 30 || edit.Revision != 1 {
		t.Fatalf("guarded edit: %+v", edit)
	}
	// Another tab sets 35; Undo of the edit above still carries the revision the edit left.
	if other := decodeProfile(t, patch(`{"daily_points_per_day":35}`)); other.Revision != 2 {
		t.Fatalf("unguarded edit: %+v", other)
	}
	written := updates()
	stale := patch(fmt.Sprintf(`{"daily_points_per_day":null,"expected_revision":%d}`, edit.Revision))
	var refused struct{ Errors map[string]string }
	if stale.Code != 409 || json.Unmarshal(stale.Body.Bytes(), &refused) != nil || refused.Errors["revision"] == "" {
		t.Fatalf("stale undo: HTTP %d %s", stale.Code, stale.Body.String())
	}
	if kept := decodeProfile(t, req(t, h, a, "GET", "/api/me/profile", "", nil)); kept.DailyPointsPerDay == nil || *kept.DailyPointsPerDay != 35 || kept.Revision != 2 || updates() != written {
		t.Fatalf("a refused undo wrote: %+v, %d events after %d", kept, updates(), written)
	}
	if undo := decodeProfile(t, patch(`{"daily_points_per_day":null,"expected_revision":2}`)); undo.DailyPointsPerDay != nil || undo.Revision != 3 {
		t.Fatalf("current undo: %+v", undo)
	}
	for _, body := range []string{`{"expected_revision":3}`, `{"daily_points_per_day":12,"expected_revision":-1}`, `{"daily_points_per_day":12,"expected_revision":"3"}`, `{"daily_points_per_day":12,"expected_revision":1.5}`, `{"daily_points_per_day":12,"expected_revision":null}`} {
		if got := patch(body); got.Code != 400 {
			t.Fatalf("%s: HTTP %d %s", body, got.Code, got.Body.String())
		}
	}
	if last := decodeProfile(t, req(t, h, a, "GET", "/api/me/profile", "", nil)); last.DailyPointsPerDay != nil || last.Revision != 3 {
		t.Fatalf("a refused guard wrote: %+v", last)
	}
}
func TestManifest(t *testing.T) {
	if _, err := plugins.Builtin(Plugin); err != nil {
		t.Fatal(err)
	}
}

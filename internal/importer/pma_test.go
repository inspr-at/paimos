// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestPMAFixtureAndSourceIdentity(t *testing.T) {
	routes := map[string]string{
		"/api/projects?status=all":       `[ {"id":3,"key":"DSC","name":"Client work","status":"active"} ]`,
		"/api/projects?status=deleted":   `[]`,
		"/api/users":                     `[]`,
		"/api/users?status=deleted":      `[]`,
		"/api/projects/3/issues":         `[ {"id":8,"issue_key":"DSC-8","type":"cost_unit","title":"Consulting","status":"open","rate_hourly":"125.50","historical_note":"retained"} ]`,
		"/api/projects/3/knowledge":      `[]`,
		"/api/issues?limit=100&offset=0": `{"issues":[],"has_more":false}`,
		"/api/issues/trash":              `[]`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected source request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, ok := routes[r.URL.RequestURI()]
		if !ok && (strings.HasSuffix(r.URL.Path, "/relations") || strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/history") || strings.HasSuffix(r.URL.Path, "/attachments")) {
			body, ok = "[]", true
		}
		if !ok {
			t.Errorf("unexpected fixture path %s", r.URL.RequestURI())
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	key := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(key, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewPMAAdapter("example-business", server.URL, key, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(source.InstanceID(), "pma:example-business:") {
		t.Fatal(source.InstanceID())
	}
	ctx := context.Background()
	report, err := (Importer{Source: source}).Run(ctx, "example-business", "", true)
	if err != nil || report.Counts["cost_unit"] != 1 {
		t.Fatalf("dry run %+v %v", report, err)
	}
	d := dbtest.Open(t)
	if _, err := tenantbootstrap.Create(ctx, d.App, "example-business", "Example Business"); err != nil {
		t.Fatal(err)
	}
	job := Importer{Source: source, Writer: PostgresWriter{Pool: d.App}}
	first, err := job.Run(ctx, "example-business", "", false)
	if err != nil || first.Created != 2 {
		t.Fatalf("import %+v %v", first, err)
	}
	second, err := job.Run(ctx, "example-business", "", false)
	if err != nil || second.Created != 0 || second.Updated != 0 {
		t.Fatalf("replay %+v %v", second, err)
	}
	var keyOut string
	var fields []byte
	if err := d.Admin.QueryRow(ctx, `SELECT key,fields FROM nodes WHERE key='DSC-8'`).Scan(&keyOut, &fields); err != nil {
		t.Fatal(err)
	}
	var mapped map[string]any
	if err := json.Unmarshal(fields, &mapped); err != nil {
		t.Fatal(err)
	}
	classic := mapped["classic"].(map[string]any)
	if keyOut != "DSC-8" || classic["source_id"] != source.InstanceID() || classic["historical_note"] != "retained" || classic["rate_hourly"] != "125.50" {
		t.Fatalf("PMA mapping %s %+v", keyOut, classic)
	}
	if _, err := NewPMAAdapter("", server.URL, key, server.Client()); err == nil {
		t.Fatal("empty source instance accepted")
	}
}

func TestImportSourceCollisionAndTenantIsolation(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	for _, slug := range []string{"first", "second"} {
		if _, err := tenantbootstrap.Create(ctx, d.App, slug, slug); err != nil {
			t.Fatal(err)
		}
	}
	snap := Snapshot{SourceID: "pma:one", Projects: []Project{{Record: Record{"id": 3, "name": "Project", "status": "open"}, Issues: []Record{{"id": 8, "issue_key": "DSC-8", "type": "cost_unit", "title": "Unit", "status": "open"}}}}, Details: map[int64]Details{}}
	w := PostgresWriter{Pool: d.App}
	if _, err := w.Write(ctx, snap, "first"); err != nil {
		t.Fatal(err)
	}
	snap.SourceID = "pma:two"
	if _, err := w.Write(ctx, snap, "first"); err == nil {
		t.Fatal("source collision accepted")
	}
	if _, err := w.Write(ctx, snap, "second"); err != nil {
		t.Fatalf("second tenant: %v", err)
	}
	var n int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE key='DSC-8'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("tenant nodes %d", n)
	}
}

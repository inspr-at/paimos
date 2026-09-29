// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestPortalPaceHistoryMatchesLink(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytesRepeat())
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantID := makeTenant(t, d, "pace-bind", "Pace Bind")
	admin := makePerson(t, d, tenantID, "Pace Admin", "admin")
	insertNode(t, d, tenantID, "PPR-1", "portal_product", "Harbour catalog", "Public summary.", "published", "", "{}")
	projectA := insertNode(t, d, tenantID, "PRJ-1", "project", "SECRET-PACE-A", "SECRET-PACE-BODY-A", "open", "", "{}")
	projectB := insertNode(t, d, tenantID, "PRJ-2", "project", "SECRET-PACE-B", "SECRET-PACE-BODY-B", "open", "", "{}")
	setPortal(t, d, tenantID, true)

	const ip = "203.0.113.44:1000"
	historyBody := func(project string, revision int64, on bool) string {
		flag := "false"
		if on {
			flag = "true"
		}
		return `{"release_history":` + flag + `,"project_id":"` + project + `","revision":` + strconv.FormatInt(revision, 10) + `}`
	}
	requireRow := func(project string, history bool, revision int64) {
		t.Helper()
		gotProject, gotHistory, gotRevision, found := paceLinkRow(t, d, tenantID)
		if !found || gotProject != project || gotHistory != history || gotRevision != revision {
			t.Fatalf("pace row found=%v project=%s history=%v revision=%d; want %s %v %d", found, gotProject, gotHistory, gotRevision, project, history, revision)
		}
	}

	linked := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectA+`"}`, ip, &admin, nil, nil))
	if linked.ProjectID != projectA || linked.ReleaseHistory || linked.Revision != 1 || linked.ProjectTitle != "SECRET-PACE-A" {
		t.Fatalf("link %+v", linked)
	}
	requireRow(projectA, false, 1)

	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true}`, ip, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("history alone: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true,"project_id":"`+projectA+`"}`, ip, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("history without revision: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true,"project_id":"`+projectA+`","revision":0}`, ip, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("revision zero: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", `{"release_history":true,"project_id":"`+projectA+`","revision":1.5}`, ip, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("fractional revision: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", historyBody(projectB, 1, true), ip, &admin, nil, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), paceLinkConflict) || strings.Contains(rec.Body.String(), "SECRET-PACE") {
		t.Fatalf("other project at the current revision: %d %s", rec.Code, rec.Body)
	}
	requireRow(projectA, false, 1)

	published := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", historyBody(projectA, 1, true), ip, &admin, nil, nil))
	if !published.ReleaseHistory || published.ProjectID != projectA || published.Revision != 2 {
		t.Fatalf("publish %+v", published)
	}
	requireRow(projectA, true, 2)
	if rec := f.do(http.MethodPut, "/api/portal/pace", historyBody(projectA, 1, false), ip, &admin, nil, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), paceLinkConflict) {
		t.Fatalf("stale revision: %d %s", rec.Code, rec.Body)
	}
	requireRow(projectA, true, 2)

	relinked := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectA+`"}`, ip, &admin, nil, nil))
	if !relinked.ReleaseHistory || relinked.ProjectID != projectA || relinked.Revision != 3 {
		t.Fatalf("relink %+v", relinked)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", historyBody(projectA, 2, false), ip, &admin, nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("revision from before the relink: %d %s", rec.Code, rec.Body)
	}
	requireRow(projectA, true, 3)

	switched := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectB+`"}`, ip, &admin, nil, nil))
	if switched.ReleaseHistory || switched.ProjectID != projectB || switched.Revision != 4 || switched.ProjectTitle != "SECRET-PACE-B" {
		t.Fatalf("switch %+v", switched)
	}
	raced := f.do(http.MethodPut, "/api/portal/pace", historyBody(projectA, 3, true), ip, &admin, nil, nil)
	if raced.Code != http.StatusConflict || !strings.Contains(raced.Body.String(), paceLinkConflict) || strings.Contains(raced.Body.String(), "SECRET-PACE") {
		t.Fatalf("stale project: %d %s", raced.Code, raced.Body)
	}
	requireRow(projectB, false, 4)
	current := decodeItem[paceAdmin](t, f.do(http.MethodGet, "/api/portal/pace", "", ip, &admin, nil, nil))
	if current.ProjectID != projectB || current.ReleaseHistory || current.Revision != 4 {
		t.Fatalf("link after the stale publish %+v", current)
	}
	if rec := f.do(http.MethodPut, "/api/portal/pace", historyBody(projectA, 4, true), ip, &admin, nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("current revision for the previous project: %d %s", rec.Code, rec.Body)
	}
	requireRow(projectB, false, 4)

	fresh := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", historyBody(projectB, 4, true), ip, &admin, nil, nil))
	if !fresh.ReleaseHistory || fresh.ProjectID != projectB || fresh.Revision != 5 {
		t.Fatalf("publish the link that is current %+v", fresh)
	}
	requireRow(projectB, true, 5)
	public := f.do(http.MethodGet, "/api/public/portal/pace-bind", "", "203.0.113.45:1000", nil, nil, nil)
	if public.Code != http.StatusOK || strings.Contains(public.Body.String(), `"revision"`) || strings.Contains(public.Body.String(), "SECRET-PACE") || !strings.Contains(public.Body.String(), `"release_history":true`) {
		t.Fatalf("public pace: %d %s", public.Code, public.Body)
	}

	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":null}`, ip, &admin, nil, nil))
	if _, _, _, found := paceLinkRow(t, d, tenantID); found {
		t.Fatal("cleared link left a pace row")
	}
	cleared := f.do(http.MethodPut, "/api/portal/pace", historyBody(projectB, 5, true), ip, &admin, nil, nil)
	if cleared.Code != http.StatusConflict || !strings.Contains(cleared.Body.String(), paceLinkConflict) {
		t.Fatalf("history after clear: %d %s", cleared.Code, cleared.Body)
	}
	if _, _, _, found := paceLinkRow(t, d, tenantID); found {
		t.Fatal("rejected history recreated the link")
	}
}

func TestPortalPaceClearAndRelinkRejectsStaleHistory(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytesRepeat())
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantID := makeTenant(t, d, "pace-reissue", "Pace Reissue")
	admin := makePerson(t, d, tenantID, "Pace Reissue Admin", "admin")
	insertNode(t, d, tenantID, "PPR-1", "portal_product", "Harbour catalog", "Public summary.", "published", "", "{}")
	projectA := insertNode(t, d, tenantID, "PRJ-1", "project", "SECRET-PACE-A", "SECRET-PACE-BODY-A", "open", "", "{}")
	setPortal(t, d, tenantID, true)

	const ip = "203.0.113.46:1000"
	historyBody := func(revision int64, on bool) string {
		flag := "false"
		if on {
			flag = "true"
		}
		return `{"release_history":` + flag + `,"project_id":"` + projectA + `","revision":` + strconv.FormatInt(revision, 10) + `}`
	}
	requireRow := func(history bool, revision int64) {
		t.Helper()
		gotProject, gotHistory, gotRevision, found := paceLinkRow(t, d, tenantID)
		if !found || gotProject != projectA || gotHistory != history || gotRevision != revision {
			t.Fatalf("pace row found=%v project=%s history=%v revision=%d; want %s %v %d", found, gotProject, gotHistory, gotRevision, projectA, history, revision)
		}
		if issued := paceRevisionIssued(t, d, tenantID); issued != revision {
			t.Fatalf("issued revision %d, row %d", issued, revision)
		}
	}

	linked := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectA+`"}`, ip, &admin, nil, nil))
	if linked.ProjectID != projectA || linked.ReleaseHistory || linked.Revision != 1 {
		t.Fatalf("link %+v", linked)
	}
	requireRow(false, 1)

	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":null}`, ip, &admin, nil, nil))
	if _, _, _, found := paceLinkRow(t, d, tenantID); found {
		t.Fatal("cleared link left a pace row")
	}
	if issued := paceRevisionIssued(t, d, tenantID); issued != 1 {
		t.Fatalf("clear reused or dropped the issued revision %d", issued)
	}

	relinked := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectA+`"}`, ip, &admin, nil, nil))
	if relinked.ProjectID != projectA || relinked.ReleaseHistory || relinked.Revision != 2 {
		t.Fatalf("relink after clear %+v", relinked)
	}
	stale := f.do(http.MethodPut, "/api/portal/pace", historyBody(1, true), ip, &admin, nil, nil)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), paceLinkConflict) || strings.Contains(stale.Body.String(), "SECRET-PACE") {
		t.Fatalf("stale history after clear and relink: %d %s", stale.Code, stale.Body)
	}
	requireRow(false, 2)

	published := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", historyBody(2, true), ip, &admin, nil, nil))
	if !published.ReleaseHistory || published.ProjectID != projectA || published.Revision != 3 {
		t.Fatalf("publish the recreated link %+v", published)
	}
	requireRow(true, 3)

	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":null}`, ip, &admin, nil, nil))
	if issued := paceRevisionIssued(t, d, tenantID); issued != 3 {
		t.Fatalf("clear after publish issued %d", issued)
	}
	again := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectA+`"}`, ip, &admin, nil, nil))
	if again.ReleaseHistory || again.Revision != 4 {
		t.Fatalf("second relink %+v", again)
	}
	for _, revision := range []int64{1, 2, 3} {
		rec := f.do(http.MethodPut, "/api/portal/pace", historyBody(revision, true), ip, &admin, nil, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), paceLinkConflict) {
			t.Fatalf("stale revision %d after the second relink: %d %s", revision, rec.Code, rec.Body)
		}
	}
	requireRow(false, 4)
}

// A link written before 0936 has no counter until that migration copies it.
// The copy runs as the non-bypass owner, which cannot see portal_pace
// without a tenant setting. Clear, relink, then submit the old revision.
func TestPortalPaceMigrationBackfillRejectsStaleHistory(t *testing.T) {
	ctx := t.Context()
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("dbtest cleanup: %v", err)
		}
	})

	var tenantID, projectID string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "0936_portal_pace_link_revision.sql" {
			return nil
		}
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('pace-pre-936','Pace Before') RETURNING id::text`).Scan(&tenantID); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO nodes(tenant_id,key,kind_id,title,body,state)
				SELECT $1::uuid,'PRJ-1',k.id,'SECRET-PACE-PRE','SECRET-PACE-BODY','open'
				FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug='project'
				RETURNING id::text`, tenantID).Scan(&projectID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO portal_pace(tenant_id, project_node_id) VALUES ($1::uuid, $2::uuid)`, tenantID, projectID)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	m := New(d.App, false, bytesRepeat())
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}
	insertNode(t, d, tenantID, "PPR-1", "portal_product", "Harbour catalog", "Public summary.", "published", "", "{}")
	admin := makePerson(t, d, tenantID, "Pace Before Admin", "admin")
	setPortal(t, d, tenantID, true)

	gotProject, gotHistory, gotRevision, found := paceLinkRow(t, d, tenantID)
	if !found || gotProject != projectID || gotHistory || gotRevision != 1 {
		t.Fatalf("backfilled link found=%v project=%s history=%v revision=%d", found, gotProject, gotHistory, gotRevision)
	}
	if issued := paceRevisionIssued(t, d, tenantID); issued != 1 {
		t.Fatalf("backfilled counter %d, want 1", issued)
	}

	const ip = "203.0.113.47:1000"
	history := func(revision int64) string {
		return `{"release_history":true,"project_id":"` + projectID + `","revision":` + strconv.FormatInt(revision, 10) + `}`
	}
	mustOK(t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":null}`, ip, &admin, nil, nil))
	if _, _, _, found := paceLinkRow(t, d, tenantID); found {
		t.Fatal("cleared pre-migration link left a pace row")
	}
	if issued := paceRevisionIssued(t, d, tenantID); issued != 1 {
		t.Fatalf("clear dropped the backfilled revision %d", issued)
	}

	relinked := decodeItem[paceAdmin](t, f.do(http.MethodPut, "/api/portal/pace", `{"project_id":"`+projectID+`"}`, ip, &admin, nil, nil))
	if relinked.ProjectID != projectID || relinked.ReleaseHistory || relinked.Revision != 2 || relinked.ProjectTitle != "SECRET-PACE-PRE" {
		t.Fatalf("relink after backfill %+v", relinked)
	}
	stale := f.do(http.MethodPut, "/api/portal/pace", history(1), ip, &admin, nil, nil)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), paceLinkConflict) || strings.Contains(stale.Body.String(), "SECRET-PACE") {
		t.Fatalf("stale history after a pre-migration link was cleared and relinked: %d %s", stale.Code, stale.Body)
	}
	gotProject, gotHistory, gotRevision, found = paceLinkRow(t, d, tenantID)
	if !found || gotProject != projectID || gotHistory || gotRevision != 2 {
		t.Fatalf("link after rejected stale submit found=%v project=%s history=%v revision=%d", found, gotProject, gotHistory, gotRevision)
	}
}

func paceLinkRow(t *testing.T, d *dbtest.DB, tenantID string) (project string, history bool, revision int64, found bool) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(t.Context(), `SELECT project_node_id::text, release_history, revision FROM portal_pace`).Scan(&project, &history, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return project, history, revision, found
}

func paceRevisionIssued(t *testing.T, d *dbtest.DB, tenantID string) int64 {
	t.Helper()
	var revision int64
	var found bool
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(t.Context(), `SELECT revision FROM portal_pace_revision`).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("pace revision counter missing")
	}
	return revision
}

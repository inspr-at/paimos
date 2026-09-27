// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/identity"
	"github.com/inspr-at/paimos/internal/tenant"
)

type failingProvisioner struct {
	calls, retries int
	tenantID       string
	checkCommit    func(email string) error
}

func (*failingProvisioner) Name() string       { return "Zitadel" }
func (f *failingProvisioner) TenantID() string { return f.tenantID }
func (f *failingProvisioner) EnsureUser(_ context.Context, email, _ string) (identity.Result, error) {
	f.calls++
	if err := f.checkCommit(email); err != nil {
		return identity.Result{}, err
	}
	return identity.Result{}, &identity.ProvisionError{Subject: "created-at-provider"}
}
func (f *failingProvisioner) SendInvite(_ context.Context, subject string) error {
	f.retries++
	if subject != "created-at-provider" {
		return &identity.ProvisionError{}
	}
	return nil
}

func TestInviteProvisionAfterCommitAndAuthorizedRetry(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, ownerID, memberID, roleID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('ip1-provision','IP1') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person','Owner') RETURNING id::text`, tid).Scan(&ownerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person','Member') RETURNING id::text`, tid).Scan(&memberID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key='member'`, tid).Scan(&roleID)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, ownerID, "owner")
	dbtest.BindRole(t, d, tid, memberID, "member")
	owner := tenant.Principal{ID: ownerID, TenantID: tid, Kind: tenant.Person}
	member := tenant.Principal{ID: memberID, TenantID: tid, Kind: tenant.Person}
	provider := &failingProvisioner{tenantID: tid, checkCommit: func(email string) error {
		return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM invites WHERE tenant_id=$1::uuid AND email=$2`, tid, email).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Error("provider called before invite commit")
			}
			return nil
		})
	}}
	mux := http.NewServeMux()
	NewWithProvisioner(d.App, provider).Mount(mux)
	call := func(method, path, body string, p tenant.Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(tenant.WithPrincipal(req.Context(), p)))
		return rec
	}
	noneMux := http.NewServeMux()
	New(d.App).Mount(noneMux)
	noneReq := httptest.NewRequest("POST", "/api/members/invites", strings.NewReader(`{"email":"nora@example.com","display_name":"Nora Example","workspace_role_id":"`+roleID+`","provision_account":true}`))
	noneRec := httptest.NewRecorder()
	noneMux.ServeHTTP(noneRec, noneReq.WithContext(tenant.WithPrincipal(noneReq.Context(), owner)))
	if noneRec.Code != 400 {
		t.Fatalf("none provisioner accepted request: %d", noneRec.Code)
	}
	wrong := &failingProvisioner{tenantID: "another-tenant"}
	wrongMux := http.NewServeMux()
	NewWithProvisioner(d.App, wrong).Mount(wrongMux)
	wrongReq := httptest.NewRequest("GET", "/api/members", nil)
	wrongRec := httptest.NewRecorder()
	wrongMux.ServeHTTP(wrongRec, wrongReq.WithContext(tenant.WithPrincipal(wrongReq.Context(), owner)))
	if wrongRec.Code != 200 || !strings.Contains(wrongRec.Body.String(), `"provisioner":null`) {
		t.Fatalf("foreign capability: %d %s", wrongRec.Code, wrongRec.Body.String())
	}
	wrongReq = httptest.NewRequest("POST", "/api/members/invites", strings.NewReader(`{"email":"nora@example.com","display_name":"Nora Example","workspace_role_id":"`+roleID+`","provision_account":true}`))
	wrongRec = httptest.NewRecorder()
	wrongMux.ServeHTTP(wrongRec, wrongReq.WithContext(tenant.WithPrincipal(wrongReq.Context(), owner)))
	if wrongRec.Code != 400 || wrong.calls != 0 {
		t.Fatalf("foreign provisioning: %d", wrongRec.Code)
	}
	dir := call("GET", "/api/members", "", owner)
	if dir.Code != 200 || !strings.Contains(dir.Body.String(), `"provisioner":{"name":"Zitadel"}`) {
		t.Fatalf("capability: %d %s", dir.Code, dir.Body.String())
	}
	body := `{"email":"nora@example.com","display_name":"Nora Example","workspace_role_id":"` + roleID + `","provision_account":true}`
	if rec := call("POST", "/api/members/invites", body, member); rec.Code != 403 || provider.calls != 0 {
		t.Fatalf("unprivileged provisioning: %d", rec.Code)
	}
	rec := call("POST", "/api/members/invites", body, owner)
	if rec.Code != 201 || provider.calls != 1 || strings.Contains(rec.Body.String(), "created-at-provider") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Invite  Invite        `json:"invite"`
		Account accountResult `json:"account"`
		JoinURL string        `json:"join_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Account.Status != "failed" || created.JoinURL == "" {
		t.Fatalf("create result: %+v", created.Account)
	}
	retryPath := "/api/members/invites/" + created.Invite.ID + "/provision"
	if denied := call("POST", retryPath, "", member); denied.Code != 403 || provider.retries != 0 {
		t.Fatalf("unprivileged retry: %d", denied.Code)
	}
	retried := call("POST", retryPath, "", owner)
	if retried.Code != 200 || provider.retries != 1 || !strings.Contains(retried.Body.String(), `"status":"invited"`) {
		t.Fatalf("retry: %d %s", retried.Code, retried.Body.String())
	}
	if repeated := call("POST", retryPath, "", owner); repeated.Code != 409 || provider.retries != 1 {
		t.Fatalf("repeated retry: %d", repeated.Code)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var status, subject string
		if err := tx.QueryRow(ctx, `SELECT account_status,account_subject FROM invites WHERE tenant_id=$1::uuid AND id=$2::uuid`, tid, created.Invite.ID).Scan(&status, &subject); err != nil {
			return err
		}
		if status != "invited" || subject != "created-at-provider" {
			t.Error("retry state was not saved")
		}
		rows, err := tx.Query(ctx, `SELECT type,after::text FROM events WHERE tenant_id=$1::uuid AND type LIKE 'invite.account_%' ORDER BY id`, tid)
		if err != nil {
			return err
		}
		defer rows.Close()
		types := []string{}
		for rows.Next() {
			var typ, after string
			if err := rows.Scan(&typ, &after); err != nil {
				return err
			}
			if strings.Contains(after, "created-at-provider") {
				t.Error("subject leaked into audit event")
			}
			types = append(types, typ)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if strings.Join(types, ",") != "invite.account_provision_failed,invite.account_provision_started,invite.account_provisioned" {
			t.Errorf("audit events: %v", types)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

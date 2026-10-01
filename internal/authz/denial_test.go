// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/scopecode"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestDenialLayersAndDisclosureOrder(t *testing.T) {
	agent := tenant.Principal{Kind: tenant.Agent}
	for _, tc := range []struct {
		name               string
		effective          Effective
		scope              Scope
		reason, permission string
	}{
		{"role before key", Effective{Workspace: Grant{Role: &RoleRef{Key: "viewer"}}}, Scope{}, "missing_role_permission", ""},
		{"key after role", Effective{Workspace: Grant{Permissions: []string{"harness.worker"}}}, Scope{}, "missing_key_scope", "harness.worker"},
		{"unbound project", Effective{Project: &ProjectGrant{}}, Scope{ProjectID: "untrusted-target"}, "missing_project_access", ""},
		{"project role before key", Effective{Project: &ProjectGrant{Role: &RoleRef{Key: "viewer"}}}, Scope{ProjectID: "untrusted-target"}, "missing_role_permission", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := permitEffective(agent, "harness.worker", tc.effective, tc.scope)
			if !errors.Is(err, ErrForbidden) {
				t.Fatal("lost forbidden identity")
			}
			w := httptest.NewRecorder()
			WriteForbidden(w, err)
			var body map[string]any
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["code"] != "forbidden" || body["reason_code"] != tc.reason {
				t.Fatalf("incorrect denial: %s", w.Body.String())
			}
			if tc.permission == "" {
				if _, ok := body["scope"]; ok {
					t.Fatal("scope disclosed before role authority")
				}
			} else if body["scope"] != tc.permission {
				t.Fatal("missing safe scope diagnostic")
			}
		})
	}
	w := httptest.NewRecorder()
	WriteForbidden(w, ErrForbidden)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["reason_code"] != nil || body["scope"] != nil || body["scope_label"] != nil || body["scope_code"] != nil {
		t.Fatal("generic denial leaked diagnostics")
	}
}

func TestMissingScopeNamesLabelAndCodeOnlyAfterAuthentication(t *testing.T) {
	err := permitEffective(tenant.Principal{Kind: tenant.Agent}, "events.read", Effective{Workspace: Grant{Permissions: []string{"events.read"}}}, Scope{})
	w := httptest.NewRecorder()
	WriteForbidden(w, err)
	var body map[string]string
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal("invalid response")
	}
	if w.Code != 403 || body["scope"] != "events.read" || body["scope_label"] != "See history" || !strings.Contains(body["error"], "events.read (See history)") || body["reason"] != body["error"] {
		t.Fatalf("missing actionable scope: %s", w.Body.String())
	}
	scopes, err := scopecode.Decode(body["scope_code"])
	if err != nil || len(scopes) != 1 || scopes[0] != "events.read" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("bad scope proposal")
	}
	for _, err := range []error{Require(context.Background(), "events.read", Scope{}), &denial{reason: "missing_role_permission"}, &denial{reason: "missing_project_access"}} {
		w := httptest.NewRecorder()
		WriteForbidden(w, err)
		if strings.Contains(w.Body.String(), "scope") || strings.Contains(w.Body.String(), "events.read") {
			t.Fatal("scope disclosed without scope-only denial")
		}
	}
}

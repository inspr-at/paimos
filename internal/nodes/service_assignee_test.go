// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/operatoractor"
	"github.com/inspr-at/paimos/internal/systemactor"
)

func TestServicePrincipalsCannotBeAssignees(t *testing.T) {
	p := newPrincipal(t, "no-service-assignee")
	kind := kindBySlug(t, p, "work")
	var systemID, operatorID, agentID string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		actor, err := systemactor.Ensure(t.Context(), tx, p.TenantID)
		if err != nil {
			return err
		}
		systemID = actor.ID
		if operatorID, err = operatoractor.Ensure(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','helper','{}') RETURNING id::text`, p.TenantID).Scan(&agentID)
	}); err != nil {
		t.Fatal(err)
	}
	reject := func(method, path, body string) {
		t.Helper()
		status, raw := call(t, &p, method, path, body)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), "service principals cannot be assignees") {
			t.Fatalf("%s %s: %d %s", method, path, status, raw)
		}
	}
	reject(http.MethodPost, "/api/nodes", `{"kind_id":"`+kind.ID+`","title":"System","fields":{"assignee":"`+systemID+`"}}`)
	reject(http.MethodPost, "/api/nodes", `{"kind_id":"`+kind.ID+`","title":"Operator","fields":{"assignee_id":"`+operatorID+`"}}`)
	reject(http.MethodPost, "/api/nodes", `{"kind_id":"`+kind.ID+`","title":"Object","fields":{"assignee":{"id":"`+systemID+`","name":"System"}}}`)

	node := mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"Owned","fields":{"assignee":"`+p.ID+`","priority":"low"}}`)
	reject(http.MethodPatch, "/api/nodes/"+node.ID, `{"fields":{"assignee":"`+systemID+`","priority":"low"}}`)
	reject(http.MethodPost, "/api/nodes/bulk", `{"ids":["`+node.ID+`"],"assignee":"`+systemID+`"}`)
	reject(http.MethodPost, "/api/nodes/bulk", `{"ids":["`+node.ID+`"],"assignee":"`+operatorID+`"}`)

	status, body := call(t, &p, http.MethodPost, "/api/nodes", `{"kind_id":"`+kind.ID+`","title":"Agent","fields":{"assignee":"`+agentID+`"}}`)
	created := decode[nodeJSON](t, status, body, http.StatusCreated)
	var fields map[string]any
	if json.Unmarshal(created.Fields, &fields) != nil || fields["assignee"] != agentID {
		t.Fatalf("ordinary agent: %s", created.Fields)
	}

	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields = jsonb_set(fields, '{assignee}', to_jsonb($2::text)) WHERE tenant_id=$1 AND id=$3::uuid`, p.TenantID, systemID, node.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, body = call(t, &p, http.MethodPatch, "/api/nodes/"+node.ID, `{"fields":{"assignee":"`+systemID+`","priority":"high"}}`)
	updated := decode[nodeJSON](t, status, body, http.StatusOK)
	if json.Unmarshal(updated.Fields, &fields) != nil || fields["assignee"] != systemID || fields["priority"] != "high" {
		t.Fatalf("unchanged system assignee: %s", updated.Fields)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+node.ID+`"],"priority":"medium"}`)
	if status != http.StatusOK {
		t.Fatalf("bulk priority: %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes/"+node.ID, "")
	kept := decode[nodeJSON](t, status, body, http.StatusOK)
	if json.Unmarshal(kept.Fields, &fields) != nil || fields["assignee"] != systemID || fields["priority"] != "medium" {
		t.Fatalf("bulk kept assignee: %s", kept.Fields)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+node.ID+`"],"assignee":null}`)
	cleared := decode[bulkResult](t, status, body, http.StatusOK)
	if len(cleared.Items) != 1 {
		t.Fatalf("clear: %+v %s", cleared, body)
	}
	if json.Unmarshal(cleared.Items[0].Fields, &fields) != nil || fields["assignee"] != nil {
		t.Fatalf("cleared fields: %s", cleared.Items[0].Fields)
	}
}

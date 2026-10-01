// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import "testing"

func TestAccountBillingPersonDeclarationPreserved(t *testing.T) {
	reset(t)
	admin, runner, _, token, mod := groupFixture(t)
	account := groupAccount(t, mod, admin, runner, token, "billing", "daemon-billing", "Billing", "laptop")
	if account.BillingMode != "unknown" {
		t.Fatalf("new account billing: %q", account.BillingMode)
	}
	path := "/api/agent-accounts/" + account.ID + "/metadata"
	body := map[string]any{"label": "Billing", "plan": "Paid plan", "host_label": "laptop", "allowed_model_profile_ids": []string{}, "billing_mode": "subscription"}
	var out Account
	callStatus(t, mod, &admin, "", "PUT", path, encoded(t, body), 200, &out)
	if out.BillingMode != "subscription" {
		t.Fatalf("declaration: %+v", out)
	}
	callStatus(t, mod, &runner, token, "PUT", path, encoded(t, body), 403, nil)
	delete(body, "billing_mode")
	body["plan"] = "Renamed plan"
	callStatus(t, mod, &runner, token, "PUT", path, encoded(t, body), 200, &out)
	if out.BillingMode != "subscription" {
		t.Fatal("daemon metadata erased person billing")
	}
	body["billing_mode"] = "guessed"
	callStatus(t, mod, &admin, "", "PUT", path, encoded(t, body), 400, nil)
	body["billing_mode"] = "api"
	callStatus(t, mod, &admin, "", "PUT", path, encoded(t, body), 200, &out)
	if out.BillingMode != "api" {
		t.Fatal("person could not update billing")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeroles"
)

// Risk: a role label is mistaken for launch authority or the recipe exposes
// local account/history details and silently selects LEAD's native grant.
func TestNativeRoleRecipesRequireSeparateAuthority(t *testing.T) {
	for _, role := range []string{"Tick", "OPS", "AIT"} {
		var out bytes.Buffer
		rt := &runtime{stdout: &out, jsonOut: true}
		command := rt.nativeRoleRecipe()
		if command.Name != "role-recipe" || command.minArgs != 1 || command.maxArgs != 1 || command.addFlags != nil {
			t.Fatal("recipe acquired a launch or credential flag")
		}
		if err := command.run([]string{role}); err != nil {
			t.Fatal(err)
		}
		var recipe nativeroles.Recipe
		if json.Unmarshal(out.Bytes(), &recipe) != nil || recipe.Role != role || len(recipe.RequiredGrants) < 6 || len(recipe.RequiredEvidence) < 5 || len(recipe.Cutover) != 5 || len(recipe.Rollback) != 3 || len(recipe.RemoteAttach) != 2 {
			t.Fatal("role recipe omitted its approval/ownership/rollback boundary")
		}
		if strings.Contains(out.String(), "account_home") || strings.Contains(out.String(), "native_resume_ref") || strings.Contains(out.String(), "control_capability") {
			t.Fatal("recipe contains local authority/history fields")
		}
		_, err := switchNativeRole(t.Context(), nil, nativeroles.Scope{}, nativeroles.Request{Role: role})
		var validation *nativecontract.ValidationError
		if !errors.As(err, &validation) || validation.Reason != nativecontract.ReasonQualificationMissing {
			t.Fatal("role label granted cutover without authenticated integration", err)
		}
	}
	var out bytes.Buffer
	command := (&runtime{stdout: &out}).nativeRoleRecipe()
	for _, role := range []string{"LEAD", "tick", "coordinator", "OPS; ssh foreign", ""} {
		if err := command.run([]string{role}); err == nil || out.Len() != 0 {
			t.Fatal("unknown role acquired recipe authority", role, err)
		}
	}
	if err := command.run([]string{"OPS"}); err != nil || !strings.Contains(out.String(), "Only an authorized operator") || !strings.Contains(out.String(), "Rollback:") ||
		!strings.Contains(out.String(), "Required grant: principal/owner") || !strings.Contains(out.String(), "Required evidence: G_ROLES") || !strings.Contains(out.String(), "Remote attach: approved SSH invoking aeon session attach") {
		t.Fatal("text recipe omitted the operator/rollback boundary", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/rulesimport"
)

func TestImportedPackDetailsStayOutsideSessionBudget(t *testing.T) {
	dir := t.TempDir()
	body := "## Checks\n- Keep the session line short.\n  Why: the pack is on demand.\n  Details: " + strings.Repeat("PACKTOKEN", 400) + "\n"
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal, err := rulesimport.Build(context.Background(), rulesimport.Request{Context: rulesimport.ContextProject, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if !proposal.AlwaysOn.Insert || proposal.AlwaysOn.Budget != LegacyMaxBytes || strings.Contains(proposal.AlwaysOn.Explanation, "PACKTOKEN") {
		t.Fatalf("always-on %+v", proposal.AlwaysOn)
	}
	mapped, err := rulesimport.MapDraft(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapped) != 1 || !strings.Contains(mapped[0].Details, "PACKTOKEN") || strings.Contains(mapped[0].Text, "PACKTOKEN") {
		t.Fatal("pack was not attached as details")
	}
	rendered := RenderedBody([]Rule{{Identity: mapped[0].Identity, Text: mapped[0].Text, Enabled: mapped[0].Enabled}})
	if proposal.AlwaysOn.Bytes != len(rendered) || !strings.Contains(rendered, "["+mapped[0].Identity+"]") || strings.Contains(rendered, "PACKTOKEN") {
		t.Fatalf("preview %d is not the session renderer (%d)", proposal.AlwaysOn.Bytes, len(rendered))
	}
	rule := Rule{
		Identity: mapped[0].Identity, Text: mapped[0].Text, Why: mapped[0].Why, Details: mapped[0].Details,
		Strength: mapped[0].Strength, Enabled: mapped[0].Enabled,
		Source: Source{Reference: mapped[0].Source.Reference, Revision: mapped[0].Source.Revision, Identity: mapped[0].Source.Identity, EditedHere: mapped[0].Source.EditedHere},
	}
	merged, err := Merge(testContext(), []Snapshot{floorSnapshot(), testSnapshot("imported", Scope{Layer: "project", ProjectID: testProject}, rule)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merged.Body, "PACKTOKEN") || !strings.Contains(merged.Body, "Keep the session line short.") || merged.ByteSize > MaxBytes {
		t.Fatalf("session file leaked details or exceeded %d: %d\n%s", MaxBytes, merged.ByteSize, merged.Body)
	}
}

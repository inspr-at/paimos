// SPDX-License-Identifier: AGPL-3.0-only
package rulesimport

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/markdownsource/testfixture"
)

func TestCodeExamplesNeverBecomeRules(t *testing.T) {
	for name, block := range testfixture.Blocks() {
		t.Run(name, func(t *testing.T) {
			body := "# Rules\n\n" + block + "## Real\n\n- Preserve safety.\n"
			p, err := parseDocument(SourceFile{Kind: "project"}, body, SectionAll)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.rules) != 1 || p.rules[0].text != "Preserve safety." || p.rules[0].start != strings.Count(body, "\n") {
				t.Fatalf("example imported or provenance changed: %+v", p.rules)
			}
		})
	}
}

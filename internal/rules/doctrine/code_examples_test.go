// SPDX-License-Identifier: AGPL-3.0-only
package doctrine

import (
	"testing"

	"github.com/inspr-at/paimos/internal/markdownsource/testfixture"
)

func TestHeadingAnchorsIgnoreCompleteCodeExamples(t *testing.T) {
	for name, block := range testfixture.Blocks() {
		t.Run(name, func(t *testing.T) {
			got := headingAnchors(rawLines([]byte(block + "## Changelog\n")))
			if len(got) != 1 || got[0].anchor != "changelog" {
				t.Fatalf("example consumed a real heading anchor: %+v", got)
			}
		})
	}
}

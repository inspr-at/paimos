// SPDX-License-Identifier: AGPL-3.0-only
package knowledge

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/markdownsource/testfixture"
)

func TestChangelogNeverAppendsInsideCodeExamples(t *testing.T) {
	for name, block := range testfixture.Blocks() {
		t.Run(name, func(t *testing.T) {
			body := "# Method\n\n" + block
			got, _ := appendChangelog(body, "- Accepted learning.")
			if !strings.HasPrefix(got, body) || !strings.HasSuffix(got, "## Changelog\n\n- Accepted learning.\n") {
				t.Fatalf("example changed instead of adding visible changelog: %q", got)
			}
		})
	}
}

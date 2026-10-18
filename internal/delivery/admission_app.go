// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"github.com/inspr-at/paimos/internal/crossreview"
)

// AppAdmission is explicitly enabled by host configuration. It neither
// provisions an App nor expands its installation or ruleset grants.
type AppAdmission struct{ App *crossreview.GitHubApp }

func (g AppAdmission) Status(ctx context.Context, sha string) (string, error) {
	return g.App.AdmissionStatus(ctx, sha, nil)
}
func (g AppAdmission) PostStatus(ctx context.Context, sha, state string) error {
	_, err := g.App.AdmissionStatus(ctx, sha, &state)
	return err
}
func (g AppAdmission) Enqueue(ctx context.Context, pr int64, head string) error {
	return g.App.AdmissionQueue(ctx, pr, head, true)
}
func (g AppAdmission) Dequeue(ctx context.Context, pr int64) error {
	return g.App.AdmissionQueue(ctx, pr, "", false)
}

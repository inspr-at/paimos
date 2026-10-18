// SPDX-License-Identifier: AGPL-3.0-only
package engineadmission

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/crossreview"
)

// WorkPRReader must return a complete inventory count, never a truncated page.
type WorkPRReader interface {
	Count(context.Context, string) (int, error)
}
type AppReader struct{ App *crossreview.GitHubApp }

var errWIP = errors.New("work PR inventory unavailable")

// Count borrows the existing read-only installation authority. No token, raw
// title, author, host, or arbitrary GitHub payload enters admission's events.
func (g AppReader) Count(ctx context.Context, tenantID string) (int, error) {
	if g.App == nil || !g.App.Configured(tenantID, g.App.Config.Repository) {
		return 0, errWIP
	}
	count := 0
	err := g.App.ReadInstallation(ctx, func(get func(string, any) error, _ func(int64, string) (bool, string, error)) error {
		var err error
		count, err = countOpenWorkPRs(get)
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func countOpenWorkPRs(get func(string, any) error) (int, error) {
	count := 0
	err := func() error {
		seen := map[int64]bool{}
		for page := 1; page <= 20; page++ {
			var pulls []struct {
				Number int64  `json:"number"`
				State  string `json:"state"`
				Draft  *bool  `json:"draft"`
				Head   struct {
					Ref string `json:"ref"`
				} `json:"head"`
			}
			if err := get("/pulls?state=open&per_page=100&page="+strconv.Itoa(page), &pulls); err != nil {
				return err
			}
			if len(pulls) > 100 {
				return errWIP
			}
			for _, p := range pulls {
				if p.Number <= 0 || seen[p.Number] || p.State != "open" || p.Draft == nil || len(p.Head.Ref) == 0 || len(p.Head.Ref) > 255 {
					return errWIP
				}
				seen[p.Number] = true
				if !*p.Draft && strings.HasPrefix(p.Head.Ref, "work/") {
					count++
				}
			}
			if len(pulls) < 100 {
				return nil
			}
		}
		return errWIP
	}()
	if err != nil {
		return 0, err
	}
	return count, nil
}

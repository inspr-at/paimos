// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Follow only the GitHub artifact redirect, without forwarding installation
// authority. Both compressed bytes and the caller's expanded JSON are bounded.
func (g *GitHubApp) artifactArchive(ctx context.Context, token string, id int64) ([]byte, error) {
	if id <= 0 {
		return nil, errGitHub
	}
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if g.Client != nil {
		client.Transport = g.Client.Transport
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+g.Config.Repository+"/actions/artifacts/"+strconv.FormatInt(id, 10)+"/zip", nil)
	if err != nil {
		return nil, errGitHub
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	res, err := client.Do(req)
	if err != nil {
		return nil, errGitHub
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		return nil, errGitHub
	}
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" ||
		!(strings.HasSuffix(u.Hostname(), ".blob.core.windows.net") || strings.HasSuffix(u.Hostname(), ".actions.githubusercontent.com")) {
		return nil, errGitHub
	}
	req, err = http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errGitHub
	}
	res, err = client.Do(req)
	if err != nil {
		return nil, errGitHub
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errGitHub
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, errGitHub
	}
	return raw, nil
}

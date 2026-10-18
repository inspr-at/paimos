// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"path/filepath"
	"time"
)

// RoutinePullInstallation uses only an already-approved installation. Requesting
// its repository-limited PR permission does not configure or enlarge the App.
// A missing permission refuses the operation. The callback has no general write
// API: its sole write is POST /pulls. Credentials never leave this layer.
func (g *GitHubApp) RoutinePullInstallation(ctx context.Context, repository string, write bool, fn func(func(string, any) error, func(any, any) error) error) error {
	if !g.Configured(g.Config.TenantID, repository) {
		return ErrUnavailable
	}
	jwt, err := g.jwt()
	if err != nil {
		return err
	}
	level := "read"
	if write {
		level = "write"
	}
	scopes := map[string]string{"pull_requests": level, "contents": "read", "metadata": "read"}
	var token struct {
		Token        string            `json:"token"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err = g.request(ctx, jwt, "POST", "/app/installations/"+g.Config.InstallationID+"/access_tokens", map[string]any{"repositories": []string{filepath.Base(repository)}, "permissions": scopes}, &token); err != nil {
		return err
	}
	if !appToken.MatchString(token.Token) {
		return ErrUnavailable
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = g.request(cleanup, token.Token, "DELETE", "/installation/token", nil, nil)
	}()
	if len(token.Repositories) != 1 || token.Repositories[0].FullName != repository || len(token.Permissions) != len(scopes) {
		return ErrUnavailable
	}
	for name, level := range scopes {
		if token.Permissions[name] != level {
			return ErrUnavailable
		}
	}
	return fn(func(path string, out any) error {
		return g.requestBounded(ctx, token.Token, "GET", "/repos/"+repository+path, nil, out, 4<<20)
	}, func(body, out any) error {
		if !write {
			return ErrUnavailable
		}
		return g.request(ctx, token.Token, "POST", "/repos/"+repository+"/pulls", body, out)
	})
}

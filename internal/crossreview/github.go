// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/workorders"
)

type StatusPublisher interface {
	Configured(tenantID, repository string) bool
	// "stale", nil confirms that error was posted on the reviewed SHA.
	// "stale", err invalidates the local gate but requires another post attempt.
	Publish(context.Context, string, Review, string) (string, error)
}

// AppConfig is host-owned and grants exactly one tenant/repository pair. The
// App needs statuses:write and pull_requests:read; no contents or merge rights.
type AppConfig struct{ ID, InstallationID, KeyFile, TenantID, Repository string }
type GitHubApp struct {
	Config AppConfig
	Client *http.Client
}

var appNumber = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
var appToken = regexp.MustCompile(`^[A-Za-z0-9_.-]{8,255}$`)
var errGitHub = errors.New("GitHub review status could not be confirmed")

func (g *GitHubApp) Configured(tenantID, repository string) bool {
	a := g.Config
	return appNumber.MatchString(a.ID) && appNumber.MatchString(a.InstallationID) && filepath.IsAbs(a.KeyFile) && workorders.UUID(a.TenantID) && reviewgate.ValidRepository(a.Repository) && tenantID == a.TenantID && repository == a.Repository
}
func (g *GitHubApp) request(ctx context.Context, token, method, path string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return errGitHub
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, bytes.NewReader(raw))
	if err != nil {
		return errGitHub
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	client := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if g.Client != nil {
		client = *g.Client
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if client.Timeout == 0 {
			client.Timeout = 15 * time.Second
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return errGitHub
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errGitHub
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return errGitHub
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return errGitHub
	}
	return nil
}
func (g *GitHubApp) jwt() (string, error) {
	path := g.Config.KeyFile
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return "", errGitHub
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errGitHub
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errGitHub
	}
	raw, err := io.ReadAll(io.LimitReader(f, 32769))
	if err != nil || len(raw) > 32768 {
		return "", errGitHub
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errGitHub
	}
	key, _ := x509.ParsePKCS1PrivateKey(block.Bytes)
	if key == nil {
		parsed, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 {
		return "", errGitHub
	}
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix(), "iss": g.Config.ID})
	enc := base64.RawURLEncoding.EncodeToString
	unsigned := enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errGitHub
	}
	return unsigned + "." + enc(signature), nil
}

func (g *GitHubApp) Publish(ctx context.Context, tenantID string, v Review, state string) (string, error) {
	if state != statusState(v) || !g.Configured(tenantID, v.Repository) || v.PullRequest == nil || !reviewgate.ValidSHA(v.HeadSHA) || (state != "pending" && state != "success" && state != "failure" && state != "error") {
		return "error", errGitHub
	}
	jwt, err := g.jwt()
	if err != nil {
		return "error", err
	}
	var token struct {
		Token        string            `json:"token"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	err = g.request(ctx, jwt, "POST", "/app/installations/"+g.Config.InstallationID+"/access_tokens", map[string]any{"repositories": []string{filepath.Base(v.Repository)}, "permissions": map[string]string{"statuses": "write", "pull_requests": "read"}}, &token)
	if err != nil || !appToken.MatchString(token.Token) {
		return "error", errGitHub
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = g.request(cleanup, token.Token, "DELETE", "/installation/token", nil, nil)
	}()
	if len(token.Repositories) != 1 || token.Repositories[0].FullName != v.Repository || token.Permissions["statuses"] != "write" || token.Permissions["pull_requests"] != "read" {
		return "error", errGitHub
	}
	for name, level := range token.Permissions {
		if name != "statuses" && name != "pull_requests" && !(name == "metadata" && level == "read") {
			return "error", errGitHub
		}
	}
	var pr struct {
		Number int64 `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if err = g.request(ctx, token.Token, "GET", "/repos/"+v.Repository+"/pulls/"+strconv.FormatInt(*v.PullRequest, 10), nil, &pr); err != nil {
		return "error", err
	}
	// A stale binding always revokes the reviewed SHA, including when the PR
	// moved to another head. No status is posted on the unreviewed new head.
	// Returning stale with an error keeps revocation retryable and the local
	// gate closed until GitHub accepts the error status.
	if v.GitHubStatus == "stale" || pr.Number != *v.PullRequest || pr.Base.Repo.FullName != v.Repository || pr.Head.SHA != v.HeadSHA || pr.Base.SHA != v.BaseSHA {
		err = g.request(ctx, token.Token, "POST", "/repos/"+v.Repository+"/statuses/"+v.HeadSHA, map[string]string{"context": "aeon/review", "state": "error", "description": "Review binding differs from the pull request"}, nil)
		return "stale", err
	}
	// A refresh still verifies the PR range and revokes its temporary token,
	// but need not append another identical success status on every poll.
	if state == "success" && v.GitHubStatus == "success" {
		return state, nil
	}
	// The stale branch above keeps its own binding sentence, including when the
	// pull request moved and GitHubStatus is not yet stale. Every other post
	// uses the same bounded gate reason the local reporter compares.
	err = g.request(ctx, token.Token, "POST", "/repos/"+v.Repository+"/statuses/"+v.HeadSHA, map[string]string{"context": "aeon/review", "state": state, "description": statusDescription(v, state)}, nil)
	if err != nil {
		return "error", err
	}
	return state, nil
}

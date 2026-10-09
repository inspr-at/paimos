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
	"strings"
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

// ErrNotFound is a confirmed absence of the requested GitHub resource.
// Transport failures, permission failures and partial bodies stay errGitHub.
var ErrNotFound = errors.New("GitHub resource not found")

func (g *GitHubApp) Configured(tenantID, repository string) bool {
	a := g.Config
	return appNumber.MatchString(a.ID) && appNumber.MatchString(a.InstallationID) && filepath.IsAbs(a.KeyFile) && workorders.UUID(a.TenantID) && reviewgate.ValidRepository(a.Repository) && tenantID == a.TenantID && repository == a.Repository
}
func (g *GitHubApp) request(ctx context.Context, token, method, path string, body, out any) error {
	return g.requestBounded(ctx, token, method, path, body, out, 64<<10)
}
func (g *GitHubApp) requestBounded(ctx context.Context, token, method, path string, body, out any, limit int64) error {
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
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errGitHub
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
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

// GitHubPull and GitHubCheck carry bounded observation data, never a write API.
type GitHubPull struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}
type GitHubCheck struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// ReadInstallation mints a separate token with read permissions only. Callers
// never receive it. The existing publisher's token and writes are unchanged.
func (g *GitHubApp) ReadInstallation(ctx context.Context, read func(func(string, any) error, func(int64, string) (bool, string, error)) error) error {
	return g.readInstallation(ctx, false, func(get func(string, any) error, queue func(int64, string) (bool, string, error), _ func(int64) ([]byte, error)) error {
		return read(get, queue)
	})
}

// ReadShippingInstallation additionally requests actions:read to distinguish
// failed aggregate jobs from cancelled shards. No PR, queue or actions write
// scope is requested in the shadow rollout, even if the App supports them.
func (g *GitHubApp) ReadShippingInstallation(ctx context.Context, read func(func(string, any) error, func(int64, string) (bool, string, error)) error) error {
	return g.readInstallation(ctx, true, func(get func(string, any) error, queue func(int64, string) (bool, string, error), _ func(int64) ([]byte, error)) error {
		return read(get, queue)
	})
}

// ReadArtifactInstallation uses the same existing, repository-scoped read-only
// authority as shipping. Signed artifact URLs and tokens never leave this layer.
func (g *GitHubApp) ReadArtifactInstallation(ctx context.Context, read func(func(string, any) error, func(int64) ([]byte, error)) error) error {
	return g.readInstallation(ctx, true, func(get func(string, any) error, _ func(int64, string) (bool, string, error), archive func(int64) ([]byte, error)) error {
		return read(get, archive)
	})
}

func (g *GitHubApp) readInstallation(ctx context.Context, shipping bool, read func(func(string, any) error, func(int64, string) (bool, string, error), func(int64) ([]byte, error)) error) error {
	if !g.Configured(g.Config.TenantID, g.Config.Repository) {
		return errGitHub
	}
	jwt, err := g.jwt()
	if err != nil {
		return err
	}
	var token struct {
		Token        string            `json:"token"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	scopes := map[string]string{"pull_requests": "read", "checks": "read", "contents": "read", "metadata": "read", "statuses": "read"}
	if shipping {
		scopes["actions"] = "read"
	}
	if err = g.request(ctx, jwt, "POST", "/app/installations/"+g.Config.InstallationID+"/access_tokens", map[string]any{"repositories": []string{filepath.Base(g.Config.Repository)}, "permissions": scopes}, &token); err != nil {
		return err
	}
	if !appToken.MatchString(token.Token) {
		return errGitHub
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = g.request(cleanup, token.Token, "DELETE", "/installation/token", nil, nil)
	}()
	if len(token.Repositories) != 1 || token.Repositories[0].FullName != g.Config.Repository {
		return errGitHub
	}
	for name, level := range token.Permissions {
		if scopes[name] != "read" || level != "read" {
			return errGitHub
		}
	}
	for name := range scopes {
		if token.Permissions[name] != "read" {
			return errGitHub
		}
	}
	return read(func(path string, out any) error {
		return g.requestBounded(ctx, token.Token, "GET", "/repos/"+g.Config.Repository+path, nil, out, 4<<20)
	}, func(number int64, head string) (bool, string, error) {
		if number <= 0 || number > 2147483647 || !reviewgate.ValidSHA(head) {
			return false, "", errGitHub
		}
		owner, name, _ := strings.Cut(g.Config.Repository, "/")
		var response struct {
			Data struct {
				Repository struct {
					Pull *struct {
						Number int64  `json:"number"`
						Head   string `json:"headRefOid"`
						Entry  *struct {
							ID   string `json:"id"`
							Head *struct {
								OID string `json:"oid"`
							} `json:"headCommit"`
						} `json:"mergeQueueEntry"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []json.RawMessage `json:"errors"`
		}
		query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){number headRefOid mergeQueueEntry{id headCommit{oid}}}}}`
		if err := g.request(ctx, token.Token, "POST", "/graphql", map[string]any{"query": query, "variables": map[string]any{"owner": owner, "name": name, "number": number}}, &response); err != nil {
			return false, "", err
		}
		pull := response.Data.Repository.Pull
		if len(response.Errors) > 0 || pull == nil || pull.Number != number || pull.Head != head {
			return false, "", errGitHub
		}
		if pull.Entry == nil {
			return false, "", nil
		}
		queueHead := ""
		if pull.Entry.Head != nil {
			queueHead = pull.Entry.Head.OID
			if !reviewgate.ValidSHA(queueHead) {
				return false, "", errGitHub
			}
		}
		return true, queueHead, nil
	}, func(id int64) ([]byte, error) { return g.artifactArchive(ctx, token.Token, id) })
}

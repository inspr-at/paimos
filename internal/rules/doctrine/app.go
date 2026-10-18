// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

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
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
)

// AppConfig is host policy, never tenant-editable. No key material or tokens.
// GateLogin is the independent repository gate that attests CI + cross-family
// review in a PR review; this App has no permission to manufacture that gate.
type AppConfig struct {
	ID              string
	InstallationID  string
	KeyRef          string
	TenantID        string
	GateLogin       string
	DCOAcknowledged bool
}

var digits = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var appSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}(\[bot\])?$`)

func (a AppConfig) configured() bool {
	return a.readConfigured() && a.DCOAcknowledged && loginPattern.MatchString(a.GateLogin)
}

func (a AppConfig) readConfigured() bool {
	return digits.MatchString(a.ID) && digits.MatchString(a.InstallationID) && credentialRefPattern.MatchString(a.KeyRef) && a.KeyRef != "github-app" && a.KeyRef != "host-mirror" && workorders.UUID(a.TenantID) && strings.ToLower(a.TenantID) == a.TenantID
}

func (m *Module) appClient(ctx context.Context, tenantID, repository string) (*GitHub, error) {
	return m.installationClient(ctx, tenantID, repository, false)
}

// installationClient shares the signing, exact scope and revocation boundary
// between read tokens and proposal tokens. Reads confer no publication authority.
func (m *Module) installationClient(ctx context.Context, tenantID, repository string, readOnly bool) (*GitHub, error) {
	a := m.app
	if readOnly && !a.readConfigured() {
		return nil, gitFail("the doctrine GitHub App installation is not configured")
	}
	if !readOnly && !a.configured() {
		return nil, fail(503, "app_unavailable", "Doctrine proposals need a server-provisioned GitHub App and independent review gate.")
	}
	// Host configuration alone cannot grant access to the App key. Use the same
	// operator-owned tenant/repository policy as credential-backed reads.
	if tenantID != a.TenantID || !m.repositories.IsPublic(repository) && !m.repositories.IsPrivate(repository) {
		return nil, ErrCredential
	}
	if err := m.credentials.authorize(a.KeyRef, tenantID, repository); err != nil {
		return nil, err
	}
	// A confined read at call time permits key rotation and forbids references
	// outside the credential directory, including escaping symlinks.
	if _, err := m.credentials.file(a.KeyRef); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(m.credentials.Dir)
	if err != nil {
		return nil, ErrCredential
	}
	defer root.Close()
	f, err := root.Open(a.KeyRef)
	if err != nil {
		return nil, ErrCredential
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrCredential
	}
	raw, err := io.ReadAll(io.LimitReader(f, 32769))
	if err != nil || len(raw) > 32768 {
		return nil, ErrCredential
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, ErrCredential
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if key == nil {
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if key == nil || key.N.BitLen() < 2048 {
		return nil, ErrCredential
	}
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix(), "iss": a.ID})
	enc := base64.RawURLEncoding.EncodeToString
	unsigned := enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(claims)
	sum := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return nil, ErrCredential
	}
	g := &GitHub{Client: m.client, Token: unsigned + "." + enc(signature)}
	// Resolve the real App bot identity. Never invent an author, reuse a
	// person's address, or publish the proposing tenant/principal identity.
	var app struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := g.request(ctx, "GET", "/app", nil, &app); err != nil {
		return nil, err
	}
	if strconv.FormatInt(app.ID, 10) != a.ID || !appSlugPattern.MatchString(app.Slug) {
		return nil, gitFail("GitHub did not identify the configured App")
	}

	// Narrow even an accidentally broad installation to this granted repository.
	var out struct {
		Token        string            `json:"token"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
			Private  *bool  `json:"private"`
		} `json:"repositories"`
	}
	permissions := map[string]string{"contents": "write", "pull_requests": "write"}
	if readOnly {
		permissions = map[string]string{"contents": "read"}
	}
	err = g.request(ctx, "POST", "/app/installations/"+a.InstallationID+"/access_tokens", map[string]any{
		"repositories": []string{strings.SplitN(repository, "/", 2)[1]},
		"permissions":  permissions,
	}, &out)
	if err != nil {
		return nil, err
	}
	// Once minted, revoke even a token rejected by scope/visibility validation.
	// Never send an invalid token back as an Authorization header.
	if !tokenPattern.MatchString(out.Token) {
		return nil, gitFail("the App token is invalid")
	}
	g.Token = out.Token
	ready := false
	defer func() {
		if !ready {
			g.revoke()
		}
	}()
	allowed := map[string]bool{repository: false}
	for _, r := range out.Repositories {
		if _, ok := allowed[r.FullName]; !ok {
			return nil, gitFail("the App token includes an unapproved repository")
		}
		if r.Private == nil || *r.Private != m.repositories.IsPrivate(r.FullName) {
			return nil, gitFail("the doctrine repository visibility does not match its public/private boundary")
		}
		allowed[r.FullName] = true
	}
	if len(out.Repositories) != 1 || !allowed[repository] {
		return nil, gitFail("the App token does not have the exact doctrine repository scope")
	}
	for permission, level := range permissions {
		if out.Permissions[permission] != level {
			return nil, gitFail("the App token does not have the exact doctrine permissions")
		}
	}
	for permission, level := range out.Permissions {
		if permissions[permission] != level && !(permission == "metadata" && level == "read") {
			return nil, gitFail("the App token has unexpected permissions")
		}
	}
	g.Token = out.Token
	if readOnly {
		ready = true
		return g, nil
	}
	var bot struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := g.request(ctx, "GET", "/users/"+app.Slug+"%5Bbot%5D", nil, &bot); err != nil {
		return nil, err
	}
	if bot.ID < 1 || bot.Login != app.Slug+"[bot]" || bot.Login == a.GateLogin {
		return nil, gitFail("The App must have its own bot identity and a different independent gate account")
	}
	g.botName = bot.Login
	g.botEmail = strconv.FormatInt(bot.ID, 10) + "+" + bot.Login + "@users.noreply.github.com"
	ready = true
	return g, nil
}

// request never includes remote bodies, URLs, headers, or transport errors in
// its errors. Redirects are forbidden so App credentials stay at the API host.
func (g *GitHub) request(ctx context.Context, method, path string, body, into any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return gitFail("the GitHub request could not be encoded")
		}
	}
	client := g.httpClient()
	req, err := http.NewRequestWithContext(ctx, method, githubAPI+path, bytes.NewReader(raw))
	if err != nil {
		return gitFail("the GitHub request could not be built")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	res, err := client.Do(req)
	if err != nil {
		return gitFail("GitHub did not confirm the request; refresh or retry the same proposal")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &apiError{status: res.StatusCode}
	}
	if into == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxTreeBody+1))
	if err != nil || len(data) > maxTreeBody {
		return gitFail("GitHub returned an unreadable response")
	}
	if json.Unmarshal(data, into) != nil {
		return gitFail("GitHub returned an invalid response")
	}
	return nil
}

type apiError struct{ status int }

func (e *apiError) Error() string {
	return "GitHub did not accept the operation; refresh before retrying"
}
func (e *apiError) Unwrap() error { return ErrGit }

// Revocation gets its own bounded context so request cancellation cannot leave
// an installation token live until expiry. Never log a credential or API body.
func (g *GitHub) revoke() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.request(ctx, http.MethodDelete, "/installation/token", nil, nil); err != nil {
		slog.Warn("doctrine installation token revocation was not confirmed")
	}
	g.Token = ""
}

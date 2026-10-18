// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/doctrinerepo"
	"github.com/inspr-at/paimos/internal/workorders"
)

var (
	credentialRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tokenPattern         = regexp.MustCompile(`^[A-Za-z0-9_.-]{8,255}$`)
)

// ErrCredential does not disclose whether a reference exists or why it was denied.
var ErrCredential = errors.New("credential unavailable")

// A moved marker does not revoke access to an already indexed pin. Cached
// delivery may tolerate this exact freshness failure; fetches, mutations and
// the private quotation guard continue to require authorizeSource to succeed.
var errMirrorCommitChanged = gitFail("the host mirror commit changed; the indexed pin is stale; reindex the source")

// Credentials resolves a credential reference to a read-only token. Aeon
// stores only the reference (a name such as doctrine-private-read). The token
// is a host-provisioned file named after it in Dir, the server's
// AEON_DOCTRINE_CREDENTIALS_DIR, read at fetch time and held for that fetch.
// The operator must also provision <ref>.allowlist.json with explicit grants
// pairing tenant_id and repository. Tenant configuration never grants access.
type Credentials struct {
	Dir            string
	MirrorDir      string
	mirrorReadOnly func(*os.File) bool
	app            *AppConfig
	repositories   doctrinerepo.Pair
}

type catalogCredentialsKey struct{}

// CatalogMiddleware supplies the same host-controlled grant policy to every
// catalog consumer, including publication and managed-session delivery. Without
// this context, credential-backed sources are denied (never an implicit grant).
func (c Credentials) CatalogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), catalogCredentialsKey{}, c)))
	})
}

// Token authorizes the tenant/repository pair before reading the token for ref.
func (c Credentials) Token(ref, tenantID, repository string) (string, error) {
	if ref == "github-app" || ref == "host-mirror" {
		return "", ErrCredential
	}
	if err := c.authorize(ref, tenantID, repository); err != nil {
		return "", err
	}
	file, err := c.file(ref)
	if err != nil {
		return "", err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", ErrCredential
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return "", ErrCredential
	}
	token := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	if !tokenPattern.MatchString(token) {
		return "", ErrCredential
	}
	return token, nil
}

// authorize also protects cached doctrine when the operator revokes a grant.
func (c Credentials) authorize(ref, tenantID, repository string) error {
	switch ref {
	case "github-app":
		if c.app == nil || !c.app.readConfigured() || tenantID != c.app.TenantID || !c.repositories.IsPublic(repository) && !c.repositories.IsPrivate(repository) {
			return ErrCredential
		}
		return c.authorize(c.app.KeyRef, tenantID, repository)
	case "host-mirror":
		if err := (Credentials{Dir: c.MirrorDir}).authorizeFile("host-mirror", tenantID, repository); err != nil {
			return err
		}
		root, _, err := openMirror(c.MirrorDir, repository, c.mirrorReadOnly)
		if root != nil {
			root.Close()
		}
		return err
	}
	return c.authorizeFile(ref, tenantID, repository)
}

func (c Credentials) authorizeFile(ref, tenantID, repository string) error {
	file, err := c.file(ref)
	if err != nil || !workorders.UUID(tenantID) || !repositoryPattern.MatchString(repository) {
		return ErrCredential
	}
	f, err := os.Open(file + ".allowlist.json")
	if err != nil {
		return ErrCredential
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return ErrCredential
	}
	var policy struct {
		Grants []struct {
			TenantID   string `json:"tenant_id"`
			Repository string `json:"repository"`
		} `json:"grants"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policy); err != nil || dec.Decode(new(any)) != io.EOF {
		return ErrCredential
	}
	allowed := false
	for _, grant := range policy.Grants {
		if !workorders.UUID(grant.TenantID) || !repositoryPattern.MatchString(grant.Repository) {
			return ErrCredential
		}
		if strings.EqualFold(grant.TenantID, tenantID) && strings.EqualFold(grant.Repository, repository) {
			allowed = true
		}
	}
	if !allowed {
		return ErrCredential
	}
	return nil
}

// authorizeSource also rejects a cached mirror whose host marker has moved.
func (c Credentials) authorizeSource(s Source, tenantID string) error {
	if err := c.authorize(s.CredentialRef, tenantID, s.Repository); err != nil {
		return err
	}
	if s.CredentialRef == "host-mirror" {
		root, commit, err := openMirror(c.MirrorDir, s.Repository, c.mirrorReadOnly)
		if root != nil {
			root.Close()
		}
		if err != nil {
			return err
		}
		if commit != s.Commit {
			return errMirrorCommitChanged
		}
	}
	return nil
}

func (c Credentials) file(ref string) (string, error) {
	if !credentialRefPattern.MatchString(ref) {
		return "", ErrCredential
	}
	if c.Dir == "" || !filepath.IsAbs(c.Dir) {
		return "", ErrCredential
	}
	return filepath.Join(c.Dir, ref), nil
}

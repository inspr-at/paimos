// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	refPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	shaPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Commit is one resolved commit of a doctrine repository.
type Commit struct {
	SHA         string
	CommittedAt *time.Time
}

// Entry is one blob in a commit's tree.
type Entry struct {
	Path string
	SHA  string
	Size int
}

// Reader reads a repository at an exact commit. Every call names the commit
// or blob it reads, so what it returns is immutable.
type Reader interface {
	Commit(ctx context.Context, repository, ref string) (Commit, error)
	Tree(ctx context.Context, repository, commit string) ([]Entry, error)
	Blob(ctx context.Context, repository, sha string, size int) ([]byte, error)
}

// GitHub uses the GitHub REST API. Token is resolved for this operation
// through an operator-owned tenant/repository grant; it is sent only to api.github.com and
// never logged, stored or put into an error.
type GitHub struct {
	Token    string
	Client   *http.Client
	botName  string
	botEmail string
	// beforeWrite re-checks authority in a short transaction immediately
	// before a ref, pull, merge or dispatch. Nil refuses the write.
	beforeWrite func(context.Context) error
}

// ErrGit is a failed read from the repository host. Its message is safe to
// store and show: it never holds a credential or a response body.
var ErrGit = errors.New("repository read failed")

type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }
func (e *gitError) Unwrap() error { return ErrGit }

func gitFail(format string, args ...any) error { return &gitError{msg: fmt.Sprintf(format, args...)} }

const (
	githubAPI   = "https://api.github.com"
	maxTreeBody = 8 << 20
	maxTreeRows = 20000
)

// httpClient enforces the same redirect boundary for reads and PR writes.
func (g *GitHub) httpClient() *http.Client {
	client := http.Client{Timeout: 20 * time.Second}
	if g.Client != nil {
		client = *g.Client
	}
	// Copy the client so a supplied client's policy cannot weaken this boundary,
	// and shared clients are not mutated. Redirect bodies/locations are not used.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func (g *GitHub) get(ctx context.Context, path, what string, limit int64, into any) error {
	// The only allowed origin is fixed. Tests inject a transport, never an origin.
	client := g.httpClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+path, nil)
	if err != nil {
		return gitFail("the %s request could not be built", what)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	res, err := client.Do(req)
	if err != nil {
		return gitFail("GitHub could not be reached for the %s", what)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if g.Token == "" {
			return gitFail("GitHub has no %s here, or the repository is private and needs a credential", what)
		}
		return gitFail("GitHub has no %s here, or the credential cannot read this repository", what)
	case http.StatusUnauthorized:
		return gitFail("GitHub refused the credential for the %s", what)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return gitFail("GitHub refused the %s request (%d); a rate limit or missing access", what, res.StatusCode)
	default:
		return gitFail("GitHub answered %d for the %s", res.StatusCode, what)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return gitFail("the %s could not be read", what)
	}
	if int64(len(body)) > limit {
		return gitFail("the %s is larger than Aeon reads", what)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return gitFail("GitHub answered the %s with unreadable data", what)
	}
	return nil
}

// Commit resolves a tag, branch or commit SHA to the commit it names now.
func (g *GitHub) Commit(ctx context.Context, repository, ref string) (Commit, error) {
	if !repositoryPattern.MatchString(repository) || !validRef(ref) {
		return Commit{}, gitFail("the repository or ref is not valid")
	}
	var out struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date *time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/commits/"+ref, "commit", 1<<20, &out); err != nil {
		return Commit{}, err
	}
	if !shaPattern.MatchString(out.SHA) || (shaPattern.MatchString(ref) && out.SHA != ref) {
		return Commit{}, gitFail("GitHub answered the commit with a different SHA")
	}
	return Commit{SHA: out.SHA, CommittedAt: out.Commit.Committer.Date}, nil
}

// Tree lists the blobs of commit. Symlinks and submodules are left out: the
// index reads regular files only.
func (g *GitHub) Tree(ctx context.Context, repository, commit string) ([]Entry, error) {
	if !repositoryPattern.MatchString(repository) || !shaPattern.MatchString(commit) {
		return nil, gitFail("the repository or commit is not valid")
	}
	var out struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int    `json:"size"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/git/trees/"+commit+"?recursive=1", "file list", maxTreeBody, &out); err != nil {
		return nil, err
	}
	if out.Truncated || len(out.Tree) > maxTreeRows {
		return nil, gitFail("the repository has more files than Aeon lists")
	}
	var entries []Entry
	for _, item := range out.Tree {
		if item.Type != "blob" || (item.Mode != "100644" && item.Mode != "100755") || !shaPattern.MatchString(item.SHA) {
			continue
		}
		entries = append(entries, Entry{Path: item.Path, SHA: item.SHA, Size: item.Size})
	}
	return entries, nil
}

// Blob reads one blob and proves it is exactly the named git object.
func (g *GitHub) Blob(ctx context.Context, repository, sha string, size int) ([]byte, error) {
	if !repositoryPattern.MatchString(repository) || !shaPattern.MatchString(sha) {
		return nil, gitFail("the repository or blob is not valid")
	}
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/git/blobs/"+sha, "file", int64(size)*2+4096, &out); err != nil {
		return nil, err
	}
	if out.Encoding != "base64" {
		return nil, gitFail("GitHub answered a file in an unexpected encoding")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, gitFail("GitHub answered a file that does not decode")
	}
	if BlobSHA(raw) != sha {
		return nil, gitFail("a file does not match its git object id")
	}
	return raw, nil
}

// BlobSHA is git's object id of raw as a blob.
func BlobSHA(raw []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(raw))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func validRef(ref string) bool {
	return refPattern.MatchString(ref) && !strings.Contains(ref, "..") && !strings.HasSuffix(ref, "/") && !strings.HasSuffix(ref, ".lock")
}

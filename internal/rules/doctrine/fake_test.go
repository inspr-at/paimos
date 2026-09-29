// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves the three REST reads the indexer uses from fixture
// commits. A repository listed in private answers 404 without the token.
type fakeGitHub struct {
	mu      sync.Mutex
	commits map[string]map[string]string // repo@sha -> path -> content
	refs    map[string]string            // repo@ref -> sha
	private map[string]bool
	token   string
	auth    []string // Authorization headers seen, in order
	calls   int
	down    bool
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *http.Client) {
	f := &fakeGitHub{commits: map[string]map[string]string{}, refs: map[string]string{}, private: map[string]bool{}}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatal("request left the fixed GitHub origin")
		}
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	return f, client
}

func (f *fakeGitHub) commit(repo, sha string, files map[string]string, refs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits[repo+"@"+sha] = files
	for _, ref := range refs {
		f.refs[repo+"@"+ref] = sha
	}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	if f.private[repo] && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.NotFound(w, r)
		return
	}
	switch {
	case parts[2] == "commits":
		ref := strings.Join(parts[3:], "/")
		sha := ref
		if named, ok := f.refs[repo+"@"+ref]; ok {
			sha = named
		}
		if _, ok := f.commits[repo+"@"+sha]; !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "commit": map[string]any{"committer": map[string]any{"date": "2026-09-22T10:25:19Z"}}})
	case parts[2] == "git" && parts[3] == "trees" && len(parts) == 5:
		files, ok := f.commits[repo+"@"+parts[4]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		var tree []map[string]any
		for _, p := range paths {
			tree = append(tree, map[string]any{"path": p, "mode": "100644", "type": "blob", "sha": BlobSHA([]byte(files[p])), "size": len(files[p])})
		}
		tree = append(tree, map[string]any{"path": "docs", "mode": "040000", "type": "tree", "sha": strings.Repeat("d", 40)})
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": parts[4], "tree": tree, "truncated": false})
	case parts[2] == "git" && parts[3] == "blobs" && len(parts) == 5:
		for key, files := range f.commits {
			if !strings.HasPrefix(key, repo+"@") {
				continue
			}
			for _, content := range files {
				if BlobSHA([]byte(content)) == parts[4] {
					enc := base64.StdEncoding.EncodeToString([]byte(content))
					var wrapped strings.Builder
					for len(enc) > 60 {
						wrapped.WriteString(enc[:60] + "\n")
						enc = enc[60:]
					}
					wrapped.WriteString(enc)
					_ = json.NewEncoder(w).Encode(map[string]any{"sha": parts[4], "encoding": "base64", "content": wrapped.String()})
					return
				}
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	maxPendingCommits = 250
	maxPendingBytes   = 4 << 20
	maxPendingKeys    = 1000
	pendingTTL        = 2 * time.Minute
)

var (
	pendingSHA  = regexp.MustCompile(`^[a-f0-9]{40}$`)
	pendingRepo = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
)

// PendingReleaseChanges reports confirmed main changes separately from an
// exact count. A failed or truncated lookup never masquerades as zero changes.
type PendingReleaseChanges struct {
	LiveVersion string    `json:"live_version"`
	BaseCommit  string    `json:"base_commit"`
	HeadCommit  string    `json:"head_commit"`
	CheckedAt   time.Time `json:"checked_at"`
	Source      string    `json:"source"`
	Status      string    `json:"status"`
	Total       *int      `json:"total"`
	KnownTotal  int       `json:"known_total"`
	Changes     []Change  `json:"changes"`
	NextCursor  *string   `json:"next_cursor"`
	Unavailable []string  `json:"unavailable"`
}

// The cache contains only public commit evidence. Tenant classification is
// applied to a fresh response after reading it, never saved into this cache.
type pendingSource struct {
	gate   chan struct{}
	github *GitHub
	now    func() time.Time
	cached *PendingReleaseChanges
}

func newPendingSource() *pendingSource {
	return &pendingSource{gate: make(chan struct{}, 1), github: &GitHub{}, now: time.Now}
}

func (m *Module) pendingChanges(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	p, _ := tenant.PrincipalFrom(r.Context())
	if p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Bound query decoding before constructing its map or touching upstream.
	if len(r.URL.RawQuery) > 1024 {
		httpapi.WriteError(w, http.StatusBadRequest, "page query too large")
		return
	}
	q := r.URL.Query()
	limit := 50
	if raw, exists := q["limit"]; exists {
		var err error
		if len(raw) != 1 {
			httpapi.WriteError(w, http.StatusBadRequest, "limit must occur once")
			return
		}
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 100 {
			httpapi.WriteError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
	}
	var head, after string
	if raw, exists := q["cursor"]; exists {
		if len(raw) != 1 || len(raw[0]) > 256 {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid pending cursor")
			return
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw[0])
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 2 || !pendingSHA.MatchString(parts[0]) || !pendingSHA.MatchString(parts[1]) {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid pending cursor")
			return
		}
		head, after = parts[0], parts[1]
	}
	base := ""
	for _, rel := range m.history.Releases {
		if rel.Version == m.current && rel.State == StatePublished {
			base = rel.Evidence.SourceCommit
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result := m.pending.snapshot(ctx, m.history.Repository, m.current, base)
	if head != "" && (result.Status == "unavailable" || head != result.HeadCommit) {
		httpapi.WriteError(w, http.StatusConflict, "pending snapshot changed; restart without a cursor")
		return
	}
	start := 0
	if after != "" {
		found := false
		for i, change := range result.Changes {
			if change.Commit == after {
				start, found = i+1, true
				break
			}
		}
		if !found {
			httpapi.WriteError(w, http.StatusBadRequest, "cursor commit is not in the pending snapshot")
			return
		}
	}
	end := min(start+limit, len(result.Changes))
	result.Changes = append([]Change{}, result.Changes[start:end]...)
	if end < result.KnownTotal {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(result.HeadCommit + ":" + result.Changes[len(result.Changes)-1].Commit))
		result.NextCursor = &cursor
	}
	var meta map[string]TicketMeta
	if m.tickets != nil && len(result.Changes) > 0 {
		keys := []string{}
		for _, change := range result.Changes {
			keys = append(keys, change.Tickets...)
		}
		keys = uniqueTicketKeys(keys)
		if len(keys) > maxPendingKeys {
			keys = keys[:maxPendingKeys]
			result.Status, result.Total = "partial", nil
			result.Unavailable = append(result.Unavailable, "Ticket classification exceeds the bounded key list; remaining groups use commit types.")
		}
		var err error
		meta, err = m.tickets(ctx, p.TenantID, keys)
		if err != nil {
			result.Status, result.Total = "partial", nil
			result.Unavailable = append(result.Unavailable, "Ticket classification could not be read; groups use commit types.")
		}
	}
	for i := range result.Changes {
		// Classify like release groups, but never copy live ticket text.
		result.Changes[i].Group = GroupChange(result.Changes[i].Subject, result.Changes[i].Tickets, meta)
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (s *pendingSource) snapshot(ctx context.Context, repository, version, base string) PendingReleaseChanges {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return pendingUnavailable(version, base, s.now(), "The main comparison could not be read in time.")
	}
	now := s.now().UTC()
	if s.cached != nil && s.cached.LiveVersion == version && s.cached.BaseCommit == base && now.Sub(s.cached.CheckedAt) < pendingTTL {
		return *s.cached
	}
	result := s.load(ctx, repository, version, base, now)
	s.cached = &result
	return result
}

func pendingUnavailable(version, base string, at time.Time, reason string) PendingReleaseChanges {
	return PendingReleaseChanges{LiveVersion: version, BaseCommit: base, CheckedAt: at.UTC(), Source: "github-main", Status: "unavailable", Changes: []Change{}, Unavailable: []string{reason}}
}

type pendingCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

func (s *pendingSource) load(ctx context.Context, repository, version, base string, at time.Time) PendingReleaseChanges {
	unavailable := func(reason string) PendingReleaseChanges { return pendingUnavailable(version, base, at, reason) }
	if !pendingSHA.MatchString(base) || !pendingRepo.MatchString(repository) {
		return unavailable("The running release has no published source commit or repository evidence.")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := s.get(ctx, "/repos/"+repository+"/git/ref/heads/main", 64<<10, &ref); err != nil || !pendingSHA.MatchString(ref.Object.SHA) {
		return unavailable("The current main source commit could not be read from GitHub.")
	}
	var comparison struct {
		Status       string          `json:"status"`
		TotalCommits int             `json:"total_commits"`
		Commits      []pendingCommit `json:"commits"`
		BaseCommit   pendingCommit   `json:"base_commit"`
	}
	// No paging parameters: GitHub documents a 250-commit bounded comparison.
	// The main head is resolved first so every page uses immutable evidence.
	if err := s.get(ctx, "/repos/"+repository+"/compare/"+base+"..."+ref.Object.SHA, maxPendingBytes, &comparison); err != nil {
		return unavailable("The main comparison could not be read from GitHub within its size and time limits.")
	}
	if comparison.BaseCommit.SHA != base || (comparison.Status != "ahead" && comparison.Status != "identical") || comparison.TotalCommits < 0 || len(comparison.Commits) > maxPendingCommits || len(comparison.Commits) > comparison.TotalCommits {
		return unavailable("GitHub could not confirm that main contains the running release source commit.")
	}
	result := PendingReleaseChanges{LiveVersion: version, BaseCommit: base, HeadCommit: ref.Object.SHA, CheckedAt: at, Source: "github-main", Status: "available", Changes: []Change{}, Unavailable: []string{}}
	seen := map[string]bool{}
	for i := len(comparison.Commits) - 1; i >= 0; i-- {
		commit := comparison.Commits[i]
		if !pendingSHA.MatchString(commit.SHA) || commit.SHA == base || seen[commit.SHA] || commit.Commit.Author.Date.IsZero() || len(commit.Parents) == 0 {
			return unavailable("GitHub returned incomplete commit evidence for the main comparison.")
		}
		seen[commit.SHA] = true
		if len(commit.Parents) > 1 {
			continue
		}
		subject := strings.TrimSpace(strings.SplitN(commit.Commit.Message, "\n", 2)[0])
		if subject == "" || len(subject) > 4096 {
			return unavailable("GitHub returned incomplete or oversized commit subjects.")
		}
		kind, scope := Classify(subject)
		if kind == "release" {
			continue
		}
		result.Changes = append(result.Changes, Change{Commit: commit.SHA, Subject: subject, Type: kind, Scope: scope, Tickets: Tickets(scope, subject), At: commit.Commit.Author.Date.UTC().Format(time.RFC3339)})
	}
	result.KnownTotal = len(result.Changes)
	if len(comparison.Commits) < comparison.TotalCommits {
		result.Status = "partial"
		result.Unavailable = append(result.Unavailable, "The main comparison exceeds the bounded commit list; the confirmed count is a lower bound.")
	} else {
		total := result.KnownTotal
		result.Total = &total
	}
	return result
}

// get bounds compressed/decoded response bytes before JSON allocation. The
// production client has no redirects and no credentials; only public evidence
// is queried. Upstream response bodies and credential values are never logged.
func (s *pendingSource) get(ctx context.Context, path string, maxBytes int64, into any) error {
	base := s.github.API
	if base == "" {
		base = "https://api.github.com"
	}
	client := s.github.Client
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("invalid GitHub request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return fmt.Errorf("GitHub response unavailable or too large")
	}
	return json.Unmarshal(raw, into)
}

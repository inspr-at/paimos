// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const apiTimeout = 30 * time.Second

// AuthenticatedEvent cannot be constructed from candidate plan/report data.
// GitHub signs the original body; live API reads establish its current subject.
type AuthenticatedEvent struct {
	name, delivery string
	body           webhook
}

type webhook struct {
	Action     string `json:"action"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		ID   int64  `json:"id"`
		Name string `json:"full_name"`
	} `json:"repository"`
	Pull  PullState `json:"pull_request"`
	Group struct {
		Base    string `json:"base_sha"`
		Head    string `json:"head_sha"`
		BaseRef string `json:"base_ref"`
		HeadRef string `json:"head_ref"`
	} `json:"merge_group"`
}

var deliveryPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,128}$`)
var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
var queuePattern = regexp.MustCompile(`^refs/heads/gh-readonly-queue/main/[a-zA-Z0-9_./-]+$`)

// AuthenticateEvent deliberately accepts only the events currently resolvable
// without candidate dispatch inputs. Unknown/manual events cannot mint a plan.
func AuthenticateEvent(name, delivery, signature string, raw, secret []byte) (AuthenticatedEvent, error) {
	if len(secret) < 32 || len(raw) == 0 || len(raw) > 2<<20 || !deliveryPattern.MatchString(delivery) ||
		(name != "pull_request" && name != "merge_group" && name != "push") {
		return AuthenticatedEvent{}, fmt.Errorf("invalid authenticated event envelope")
	}
	if !strings.HasPrefix(signature, "sha256=") || len(signature) != 71 {
		return AuthenticatedEvent{}, fmt.Errorf("invalid webhook signature")
	}
	want, err := hex.DecodeString(signature[7:])
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write(raw)
	if err != nil || !hmac.Equal(want, m.Sum(nil)) {
		return AuthenticatedEvent{}, fmt.Errorf("invalid webhook signature")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueJSON(d); err != nil {
		return AuthenticatedEvent{}, fmt.Errorf("invalid webhook JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return AuthenticatedEvent{}, fmt.Errorf("trailing webhook JSON")
	}
	var body webhook
	if err := json.Unmarshal(raw, &body); err != nil {
		return AuthenticatedEvent{}, fmt.Errorf("invalid webhook JSON")
	}
	return AuthenticatedEvent{name: name, delivery: delivery, body: body}, nil
}

type PullState struct {
	Number      int64  `json:"number"`
	State       string `json:"state"`
	MergeCommit string `json:"merge_commit_sha"`
	Base        struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			Name string `json:"full_name"`
			ID   int64  `json:"id"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// GitHubReads is read-only. No publication token or API is given to execution.
type GitHubReads interface {
	Repository(context.Context) (int64, string, error)
	Ref(context.Context, string) (string, error)
	Pull(context.Context, int64) (PullState, error)
	Pulls(context.Context) ([]PullState, error)
}

// GitHubReader makes only bounded GET requests, with redirects disabled and
// errors redacted. The token is never included in an error, record or child env.
type GitHubReader struct {
	repository, token string
	client            *http.Client
}

func NewGitHubReader(repository, token string) (*GitHubReader, error) {
	if !repoPattern.MatchString(repository) || token == "" {
		return nil, fmt.Errorf("GitHub read configuration required")
	}
	return &GitHubReader{repository: repository, token: token, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (g *GitHubReader) get(ctx context.Context, path string, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+g.repository+path, nil)
	if err != nil {
		return fmt.Errorf("GitHub read unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub read unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub read refused (HTTP %d)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return fmt.Errorf("GitHub response limit")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid GitHub response")
	}
	return nil
}

func (g *GitHubReader) Repository(ctx context.Context) (int64, string, error) {
	var r struct {
		ID   int64  `json:"id"`
		Name string `json:"full_name"`
	}
	err := g.get(ctx, "", &r)
	return r.ID, r.Name, err
}
func (g *GitHubReader) Ref(ctx context.Context, ref string) (string, error) {
	if ref != "refs/heads/main" && (!queuePattern.MatchString(ref) || strings.Contains(ref, "..")) {
		return "", fmt.Errorf("unsupported GitHub ref")
	}
	var r struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	err := g.get(ctx, "/git/ref/"+url.PathEscape(strings.TrimPrefix(ref, "refs/")), &r)
	if err != nil {
		return "", err
	}
	if r.Ref != ref || r.Object.Type != "commit" || !objectID.MatchString(r.Object.SHA) {
		return "", fmt.Errorf("invalid GitHub ref binding")
	}
	return r.Object.SHA, nil
}
func (g *GitHubReader) Pull(ctx context.Context, number int64) (PullState, error) {
	if number <= 0 {
		return PullState{}, fmt.Errorf("invalid PR number")
	}
	var p PullState
	err := g.get(ctx, fmt.Sprintf("/pulls/%d", number), &p)
	if err == nil && p.Number != number {
		err = fmt.Errorf("GitHub PR identity mismatch")
	}
	return p, err
}
func (g *GitHubReader) Pulls(ctx context.Context) ([]PullState, error) {
	all := []PullState{}
	for page := 1; page <= 20; page++ {
		var rows []PullState
		if err := g.get(ctx, fmt.Sprintf("/pulls?state=open&base=main&per_page=100&page=%d", page), &rows); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, fmt.Errorf("invalid GitHub PR inventory")
		}
		all = append(all, rows...)
		if len(rows) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GitHub PR inventory limit; incomplete group refused")
}

type AuthorityConfig struct {
	RepositoryID      int64  `json:"repository_id"`
	Repository        string `json:"repository"`
	Policy            Pin    `json:"policy"`
	EnvironmentDigest string `json:"environment_digest"`
	// Zero leaves the App unprovisioned. Actions and the review App are refused.
	VerifierAppID int64 `json:"verifier_app_id"`
}

// DecodeControllerInput rejects duplicate keys and unknown fields. JSON
// validity alone never approves the supplied bytes.
func DecodeControllerInput(raw []byte, dst any) error {
	if len(raw) > 2<<20 {
		return fmt.Errorf("controller input limit")
	}
	return decodeStrict(raw, dst)
}

// Authority owns policy, binding and generations outside candidate execution.
// This implementation has no check writer and cannot enable omissions. Its
// replay/generation lock is process-local; durable production ingress is G/E work.
type Authority struct {
	config     AuthorityConfig
	repo       *Repository
	api        GitHubReads
	mu         sync.Mutex
	deliveries map[string]bool
	generation map[string]int64
}

func NewAuthority(config AuthorityConfig, r *Repository, api GitHubReads) (*Authority, error) {
	if r == nil || api == nil || config.RepositoryID <= 0 || !repoPattern.MatchString(config.Repository) || !digestID.MatchString(config.EnvironmentDigest) ||
		!objectID.MatchString(config.Policy.Commit) || !digestID.MatchString(config.Policy.Digest) || config.VerifierAppID < 0 || config.VerifierAppID == 15368 || config.VerifierAppID == 5134402 {
		return nil, fmt.Errorf("invalid independent shadow authority configuration")
	}
	return &Authority{config: config, repo: r, api: api, deliveries: map[string]bool{}, generation: map[string]int64{}}, nil
}

func validPull(p PullState, c AuthorityConfig) bool {
	return p.Number > 0 && p.State == "open" && p.Base.Ref == "main" && p.Base.Repo.Name == c.Repository && p.Base.Repo.ID == c.RepositoryID && objectID.MatchString(p.Head.SHA) && objectID.MatchString(p.Base.SHA) && objectID.MatchString(p.MergeCommit)
}

func (r *Repository) parents(ctx context.Context, commit string) ([]string, error) {
	if !objectID.MatchString(commit) {
		return nil, fmt.Errorf("immutable commit required")
	}
	b, err := r.read(ctx, nil, "show", "-s", "--format=%P", commit)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	for _, id := range ids {
		if !objectID.MatchString(id) {
			return nil, fmt.Errorf("invalid Git parents")
		}
	}
	return ids, nil
}

func (a *Authority) resolve(ctx context.Context, e AuthenticatedEvent, generation int64) (Binding, error) {
	if e.delivery == "" {
		return Binding{}, fmt.Errorf("authenticated event required")
	}
	id, name, err := a.api.Repository(ctx)
	if err != nil {
		return Binding{}, err
	}
	if id != a.config.RepositoryID || name != a.config.Repository || e.body.Repository.ID != id || e.body.Repository.Name != name {
		return Binding{}, fmt.Errorf("repository binding mismatch")
	}
	main, err := a.api.Ref(ctx, "refs/heads/main")
	if err != nil || !objectID.MatchString(main) {
		return Binding{}, fmt.Errorf("current main unavailable")
	}
	b := Binding{RepositoryID: id, Event: e.name, DeliveryID: e.delivery, Generation: generation, Base: main}
	switch e.name {
	case "pull_request":
		if e.body.Action != "opened" && e.body.Action != "synchronize" && e.body.Action != "reopened" && e.body.Action != "ready_for_review" {
			return Binding{}, fmt.Errorf("unsupported PR event")
		}
		p, err := a.api.Pull(ctx, e.body.Pull.Number)
		if err != nil {
			return Binding{}, err
		}
		if !validPull(p, a.config) || p.Head.SHA != e.body.Pull.Head.SHA || p.Base.SHA != main || e.body.Pull.Base.SHA != main {
			return Binding{}, fmt.Errorf("stale or ineligible PR")
		}
		parents, err := a.repo.parents(ctx, p.MergeCommit)
		if err != nil || len(parents) != 2 || parents[0] != main || parents[1] != p.Head.SHA {
			return Binding{}, fmt.Errorf("PR merge candidate not bound to current base/head")
		}
		b.Candidate = p.MergeCommit
		b.CheckTarget = p.Head.SHA
		b.SourceHead = p.Head.SHA
		b.PR = p.Number
	case "merge_group":
		g := e.body.Group
		if e.body.Action != "checks_requested" || g.BaseRef != "refs/heads/main" || g.Base != main || !queuePattern.MatchString(g.HeadRef) || strings.Contains(g.HeadRef, "..") {
			return Binding{}, fmt.Errorf("ineligible queue event")
		}
		live, err := a.api.Ref(ctx, g.HeadRef)
		if err != nil || live != g.Head {
			return Binding{}, fmt.Errorf("replaced queue group")
		}
		pulls, err := a.api.Pulls(ctx)
		if err != nil {
			return Binding{}, err
		}
		selected := map[int64]bool{}
		current := g.Head
		for n := 0; current != main && n < 100; n++ {
			parents, err := a.repo.parents(ctx, current)
			if err != nil || len(parents) != 2 {
				return Binding{}, fmt.Errorf("unresolved queue ancestry")
			}
			found := false
			for _, p := range pulls {
				if validPull(p, a.config) && p.Base.SHA == main && p.Head.SHA == parents[1] {
					selected[p.Number] = true
					found = true
				}
			}
			if !found {
				return Binding{}, fmt.Errorf("missing or changed queue constituent")
			}
			current = parents[0]
		}
		if current != main || len(selected) == 0 {
			return Binding{}, fmt.Errorf("incomplete queue inventory")
		}
		for pr := range selected {
			b.GroupPRs = append(b.GroupPRs, pr)
		}
		sort.Slice(b.GroupPRs, func(i, j int) bool { return b.GroupPRs[i] < b.GroupPRs[j] })
		b.Candidate = g.Head
		b.CheckTarget = g.Head
		b.GroupID = g.HeadRef
	case "push":
		if e.body.Ref != "refs/heads/main" || e.body.After != main {
			return Binding{}, fmt.Errorf("stale or non-main push")
		}
		parents, err := a.repo.parents(ctx, main)
		if err != nil || len(parents) == 0 {
			return Binding{}, fmt.Errorf("main baseline unavailable")
		}
		b.Base = parents[0]
		b.Candidate = main
		b.CheckTarget = main
	default:
		return Binding{}, fmt.Errorf("unsupported authenticated event")
	}
	return b, b.validate()
}

func bindingSlot(b Binding) string {
	if b.PR > 0 {
		return fmt.Sprintf("pr/%d", b.PR)
	}
	if b.GroupID != "" {
		return b.GroupID
	}
	return "main"
}

func (a *Authority) Plan(ctx context.Context, e AuthenticatedEvent, generation int64) (Plan, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deliveries[e.delivery] {
		return Plan{}, fmt.Errorf("duplicate authenticated delivery")
	}
	b, err := a.resolve(ctx, e, generation)
	if err != nil {
		return Plan{}, err
	}
	if generation <= a.generation[bindingSlot(b)] {
		return Plan{}, fmt.Errorf("superseded controller generation")
	}
	p, err := NewPlan(ctx, a.repo, a.config.Policy, b, a.config.EnvironmentDigest)
	if err != nil {
		return Plan{}, err
	}
	a.deliveries[e.delivery] = true
	a.generation[bindingSlot(b)] = generation
	return p, nil
}

// Revalidate runs immediately before returning check proposals. A moved base,
// replaced group, closed PR, or new generation leaves every context closed.
func (a *Authority) Revalidate(ctx context.Context, e AuthenticatedEvent, p Plan) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.Policy != a.config.Policy || p.EnvironmentDigest != a.config.EnvironmentDigest || !a.deliveries[e.delivery] || a.generation[bindingSlot(p.Binding)] != p.Binding.Generation {
		return fmt.Errorf("unissued or superseded authority plan")
	}
	b, err := a.resolve(ctx, e, p.Binding.Generation)
	if err != nil {
		return err
	}
	if digest("resolved-binding", b) != digest("resolved-binding", p.Binding) {
		return fmt.Errorf("target changed during verification")
	}
	return VerifyPlan(ctx, a.repo, p)
}

type CheckProposal struct {
	Name     string   `json:"name"`
	AppID    int64    `json:"app_id"`
	Target   string   `json:"target"`
	PlanID   string   `json:"plan_id"`
	Status   string   `json:"status"`
	Total    int      `json:"total"`
	Observed int      `json:"observed"`
	Reasons  []string `json:"reasons"`
}

// Checks are a shadow reconciliation artifact, never a GitHub API write or a
// success badge. Even authenticated fresh observations cannot enable skipping.
func (a *Authority) Checks(ctx context.Context, e AuthenticatedEvent, p Plan, s *Supervisor, observations []SupervisedObservation) ([]CheckProposal, error) {
	if err := a.Revalidate(ctx, e, p); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, o := range observations {
		if s == nil {
			return nil, fmt.Errorf("external supervisor required")
		}
		if err := s.Verify(p, o); err != nil {
			return nil, err
		}
		if seen[o.Receipt.ObligationID] {
			return nil, fmt.Errorf("duplicate supervised observation")
		}
		seen[o.Receipt.ObligationID] = true
		for _, u := range p.Obligations {
			if u.ID == o.Receipt.ObligationID && o.Receipt.Result == "success" {
				counts[u.Context]++
			}
		}
	}
	totals := map[string]int{"ci/trusted": len(p.Obligations)}
	for _, u := range p.Obligations {
		totals[u.Context]++
	}
	names := []string{}
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := []CheckProposal{}
	for _, name := range names {
		reasons := []string{"shadow-only", "optimization-disabled", "no-authoritative-publication"}
		if a.config.VerifierAppID == 0 {
			reasons = append(reasons, "verifier-app-unprovisioned")
		}
		n := counts[name]
		if name == "ci/trusted" {
			for _, count := range counts {
				n += count
			}
		}
		if n != totals[name] {
			reasons = append(reasons, "missing-fresh-observations")
		}
		checks = append(checks, CheckProposal{Name: name, AppID: a.config.VerifierAppID, Target: p.Binding.CheckTarget, PlanID: p.ID, Status: "pending", Total: totals[name], Observed: n, Reasons: reasons})
	}
	// The target read after reconciliation closes the ordinary API race window.
	if err := a.Revalidate(ctx, e, p); err != nil {
		return nil, err
	}
	return checks, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// admissionInstallation reuses the configured App secret-path reader. OPS owns
// installation grants. Tokens are repository-limited, short-lived, and revoked;
// the callback and credentials are private to this package.
func (g *GitHubApp) admissionInstallation(ctx context.Context, permissions map[string]string, fn func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !g.Configured(g.Config.TenantID, g.Config.Repository) {
		return ErrUnavailable
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
	if err = g.request(ctx, jwt, "POST", "/app/installations/"+g.Config.InstallationID+"/access_tokens", map[string]any{"repositories": []string{filepath.Base(g.Config.Repository)}, "permissions": permissions}, &token); err != nil {
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
	if len(token.Repositories) != 1 || token.Repositories[0].FullName != g.Config.Repository || len(token.Permissions) != len(permissions) {
		return ErrUnavailable
	}
	for name, level := range permissions {
		if token.Permissions[name] != level {
			return ErrUnavailable
		}
	}
	return fn(token.Token)
}

// AdmissionStatus can write only the single admission context. It never uses
// the legacy review context and never accepts a caller-selected repository.
func (g *GitHubApp) AdmissionStatus(ctx context.Context, sha string, state *string) (string, error) {
	if !reviewgate.ValidSHA(sha) || state != nil && *state != "success" && *state != "failure" {
		return "", ErrUnavailable
	}
	observed := ""
	level := "read"
	if state != nil {
		level = "write"
	}
	err := g.admissionInstallation(ctx, map[string]string{"statuses": level, "metadata": "read"}, func(token string) error {
		if state != nil {
			return g.request(ctx, token, "POST", "/repos/"+g.Config.Repository+"/statuses/"+sha, map[string]string{"state": *state, "context": "aeon/admission", "description": "PAIMOS admission: " + *state}, nil)
		}
		for page := 1; page <= 20; page++ {
			var statuses []struct{ Context, State string }
			if err := g.requestBounded(ctx, token, "GET", "/repos/"+g.Config.Repository+"/commits/"+sha+"/statuses?per_page=100&page="+strconv.Itoa(page), nil, &statuses, 256<<10); err != nil {
				return err
			}
			for _, s := range statuses {
				if s.Context == "aeon/admission" {
					observed = s.State
					return nil
				}
			}
			if len(statuses) < 100 {
				return nil
			}
		}
		return ErrUnavailable
	})
	return observed, err
}

// AdmissionQueue exposes only explicit enqueue/dequeue. The expected head is
// checked locally and by GitHub's atomic enqueuePullRequest(expectedHeadOid).
func (g *GitHubApp) AdmissionQueue(ctx context.Context, number int64, head string, enqueue bool) error {
	if number < 1 || number > 2147483647 || enqueue && !reviewgate.ValidSHA(head) {
		return ErrUnavailable
	}
	return g.admissionInstallation(ctx, map[string]string{"pull_requests": "write", "contents": "write", "metadata": "read"}, func(token string) error {
		owner, name, _ := strings.Cut(g.Config.Repository, "/")
		var response struct {
			Data struct {
				Repository struct {
					Pull *struct {
						ID     string
						Number int64
						Head   string `json:"headRefOid"`
					} `json:"pullRequest"`
				}
			}
			Errors []json.RawMessage
		}
		query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){id number headRefOid}}}`
		if err := g.request(ctx, token, "POST", "/graphql", map[string]any{"query": query, "variables": map[string]any{"owner": owner, "name": name, "number": number}}, &response); err != nil {
			return err
		}
		p := response.Data.Repository.Pull
		if len(response.Errors) > 0 || p == nil || p.ID == "" || len(p.ID) > 256 || p.Number != number || enqueue && p.Head != head {
			return ErrUnavailable
		}
		mutation := `mutation($input:DequeuePullRequestInput!){dequeuePullRequest(input:$input){clientMutationId}}`
		input := map[string]any{"pullRequestId": p.ID, "clientMutationId": "aeon-admission"}
		if enqueue {
			mutation = `mutation($input:EnqueuePullRequestInput!){enqueuePullRequest(input:$input){clientMutationId}}`
			input["expectedHeadOid"] = head
		}
		var result struct {
			Data   map[string]json.RawMessage
			Errors []json.RawMessage
		}
		if err := g.request(ctx, token, "POST", "/graphql", map[string]any{"query": mutation, "variables": map[string]any{"input": input}}, &result); err != nil {
			return err
		}
		key := "dequeuePullRequest"
		if enqueue {
			key = "enqueuePullRequest"
		}
		if len(result.Errors) > 0 || len(result.Data[key]) == 0 || string(result.Data[key]) == "null" {
			return ErrUnavailable
		}
		return nil
	})
}

// AdmissionQueueMember is a live queue identity, not an approval assertion.
type AdmissionQueueMember struct {
	Number int64
	Head   string
}

// AdmissionQueueMembers resolves the prefix through the synthetic head from
// GitHub's queue inventory, including every preceding entry. A bounded missing,
// null, ambiguous or changing inventory cannot prove group membership.
func (g *GitHubApp) AdmissionQueueMembers(ctx context.Context, sha string) ([]AdmissionQueueMember, error) {
	if !reviewgate.ValidSHA(sha) {
		return nil, ErrUnavailable
	}
	var out []AdmissionQueueMember
	err := g.admissionInstallation(ctx, map[string]string{"pull_requests": "read", "contents": "read", "metadata": "read"}, func(token string) error {
		owner, name, _ := strings.Cut(g.Config.Repository, "/")
		var response struct {
			Data struct {
				Repository struct {
					Queue *struct {
						Entries *struct {
							Nodes []*struct {
								Position int
								Head     *struct{ OID string } `json:"headCommit"`
								Pull     *struct {
									Number int64
									Head   string `json:"headRefOid"`
								} `json:"pullRequest"`
							}
						}
					} `json:"mergeQueue"`
				}
			}
			Errors []json.RawMessage
		}
		query := `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){mergeQueue(branch:"main"){entries(first:100){nodes{position headCommit{oid} pullRequest{number headRefOid}}}}}}`
		if err := g.request(ctx, token, "POST", "/graphql", map[string]any{"query": query, "variables": map[string]string{"owner": owner, "name": name}}, &response); err != nil {
			return err
		}
		queue := response.Data.Repository.Queue
		if len(response.Errors) > 0 || queue == nil || queue.Entries == nil || len(queue.Entries.Nodes) == 0 || len(queue.Entries.Nodes) > 100 {
			return ErrUnavailable
		}
		through := -1
		seenPositions := map[int]bool{}
		seenPRs := map[int64]bool{}
		for _, entry := range queue.Entries.Nodes {
			if entry == nil || entry.Position < 0 || seenPositions[entry.Position] || entry.Pull == nil || entry.Pull.Number < 1 || seenPRs[entry.Pull.Number] || !reviewgate.ValidSHA(entry.Pull.Head) {
				return ErrUnavailable
			}
			seenPositions[entry.Position] = true
			seenPRs[entry.Pull.Number] = true
			if entry.Head != nil && entry.Head.OID == sha && entry.Position > through {
				through = entry.Position
			}
		}
		if through < 0 {
			return ErrUnavailable
		}
		for _, entry := range queue.Entries.Nodes {
			if entry.Position <= through {
				out = append(out, AdmissionQueueMember{Number: entry.Pull.Number, Head: entry.Pull.Head})
			}
		}
		return nil
	})
	return out, err
}

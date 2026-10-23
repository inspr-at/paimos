// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/reviewgate"
)

// MergeQueueProof reads historical queue provenance for one canonical merge.
// The fixed GraphQL query has no mutation capability. Its installation token
// is restricted to the configured repository and existing read grants.
func (g *GitHubApp) MergeQueueProof(ctx context.Context, number int64, sha string) (bool, error) {
	if !g.Configured(g.Config.TenantID, g.Config.Repository) || number < 1 || number > 2147483647 || !reviewgate.ValidSHA(sha) {
		return false, errGitHub
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	jwt, err := g.jwt()
	if err != nil {
		return false, err
	}
	var queued bool
	err = g.withReadToken(ctx, jwt, map[string]string{"pull_requests": "read", "metadata": "read"}, func(token string) error {
		var err error
		queued, err = readMergeQueueProof(number, sha, func(cursor *string, out any) error {
			owner, name, _ := strings.Cut(g.Config.Repository, "/")
			query := `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){number merged mergedAt mergeCommit{oid} timelineItems(first:100,after:$cursor,itemTypes:[ADDED_TO_MERGE_QUEUE_EVENT,REMOVED_FROM_MERGE_QUEUE_EVENT]){pageInfo{hasNextPage endCursor} nodes{__typename ... on AddedToMergeQueueEvent{id createdAt} ... on RemovedFromMergeQueueEvent{id createdAt reason}}}}}}`
			return g.requestBounded(ctx, token, "POST", "/graphql", map[string]any{"query": query,
				"variables": map[string]any{"owner": owner, "name": name, "number": number, "cursor": cursor}}, out, 256<<10)
		})
		return err
	})
	return queued, err
}

type queueHistoryEvent struct {
	Type   string    `json:"__typename"`
	ID     string    `json:"id"`
	At     time.Time `json:"createdAt"`
	Reason string    `json:"reason"`
}

func readMergeQueueProof(number int64, sha string, page func(*string, any) error) (bool, error) {
	var cursor *string
	var previous, mergedAt time.Time
	active, merged := false, false
	seen := map[string]bool{}
	cursors := map[string]bool{}
	for n := 0; n < 10; n++ {
		var response struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Repository struct {
					Pull *struct {
						Number int64     `json:"number"`
						Merged bool      `json:"merged"`
						At     time.Time `json:"mergedAt"`
						Commit struct {
							OID string `json:"oid"`
						} `json:"mergeCommit"`
						Timeline struct {
							Nodes []queueHistoryEvent `json:"nodes"`
							Info  *struct {
								More *bool   `json:"hasNextPage"`
								End  *string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"timelineItems"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := page(cursor, &response); err != nil {
			return false, err
		}
		p := response.Data.Repository.Pull
		if len(response.Errors) != 0 || p == nil || p.Number != number || !p.Merged || p.Commit.OID != sha || p.At.IsZero() ||
			p.Timeline.Info == nil || p.Timeline.Info.More == nil || p.Timeline.Nodes == nil || len(p.Timeline.Nodes) > 100 || !mergedAt.IsZero() && !mergedAt.Equal(p.At) {
			return false, errGitHub
		}
		mergedAt = p.At
		for _, e := range p.Timeline.Nodes {
			if e.ID == "" || len(e.ID) > 200 || seen[e.ID] || e.At.IsZero() || e.At.Before(previous) || merged {
				return false, errGitHub
			}
			seen[e.ID], previous = true, e.At
			switch e.Type {
			case "AddedToMergeQueueEvent":
				if active || e.At.After(mergedAt) {
					return false, errGitHub
				}
				active = true
			case "RemovedFromMergeQueueEvent":
				if !active || len(e.Reason) > 100 {
					return false, errGitHub
				}
				merged, active = strings.EqualFold(e.Reason, "merged"), false
			default:
				return false, errGitHub
			}
		}
		if !*p.Timeline.Info.More {
			return merged, nil
		}
		end := p.Timeline.Info.End
		if end == nil || *end == "" || len(*end) > 1024 || cursors[*end] || len(p.Timeline.Nodes) == 0 {
			return false, errGitHub
		}
		cursors[*end], cursor = true, end
	}
	return false, errGitHub // A truncated timeline is not absence or proof.
}

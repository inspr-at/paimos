// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type releaseListArgs struct {
	ProjectID string `json:"project_id"`
	State     string `json:"state,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type releaseGetArgs struct {
	ProjectID string `json:"project_id"`
	ReleaseID string `json:"release_id"`
}
type releaseItemsArgs struct {
	ProjectID      string `json:"project_id"`
	ReleaseID      string `json:"release_id"`
	Through        string `json:"through,omitempty"`
	CompletedLater bool   `json:"completed_later,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}
type backlogListArgs struct {
	ProjectID         string `json:"project_id"`
	Part              string `json:"part,omitempty"`
	CompletedUnplaced bool   `json:"completed_unplaced,omitempty"`
	Cursor            string `json:"cursor,omitempty"`
	Limit             int    `json:"limit,omitempty"`
}
type shipsInPlaceArgs struct {
	ItemID                  string  `json:"item_id"`
	ProjectID               string  `json:"expected_project_id"`
	ReleaseID               *string `json:"release_id"`
	ExpectedRevision        int64   `json:"expected_revision"`
	ExpectedReleaseRevision int64   `json:"expected_release_revision,omitempty"`
	BeforeID                string  `json:"before_id,omitempty"`
	AfterID                 string  `json:"after_id,omitempty"`
}

func (rt *runtime) addReleaseTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "release_list", Description: "List a bounded release page in one project, with progress and inherited settings."}, rt.toolReleaseList)
	mcp.AddTool(s, &mcp.Tool{Name: "release_get", Description: "Read a project release, lifecycle and build summary."}, rt.toolReleaseGet)
	mcp.AddTool(s, &mcp.Tool{Name: "release_items", Description: "Read release placement and captured revisions; optionally include earlier releases or completed later work."}, rt.toolReleaseItems)
	mcp.AddTool(s, &mcp.Tool{Name: "backlog_list", Description: "Read ranked backlog, oldest-first tail or bounded completed-unplaced recovery evidence."}, rt.toolBacklogList)
	mcp.AddTool(s, &mcp.Tool{Name: "ships_in_place", Description: "Move one item with project and revision fences. Agent promotion and flag changes are refused; request earlier placement through the decision desk."}, rt.toolShipsInPlace)
	mcp.AddTool(s, &mcp.Tool{Name: "release_build_status", Description: "Read release lifecycle and authorization. Budget outlook remains unknown until the build engine provides it."}, rt.toolReleaseGet)
}
func (rt *runtime) releaseToolRead(ctx context.Context, path string) (*mcp.CallToolResult, any, error) {
	var raw json.RawMessage
	if e := rt.doCtx(ctx, http.MethodGet, path, nil, &raw); e != nil {
		return nil, nil, e
	}
	var out any
	if e := json.Unmarshal(raw, &out); e != nil {
		return nil, nil, e
	}
	return nil, out, nil
}
func toolPage(project, cursor string, limit, max int) (url.Values, error) {
	if !validUUID(project) || len(cursor) > 2048 || limit < 0 || limit > max {
		return nil, errors.New("invalid project or page")
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	return q, nil
}
func (rt *runtime) toolReleaseList(ctx context.Context, _ *mcp.CallToolRequest, in releaseListArgs) (*mcp.CallToolResult, any, error) {
	q, e := toolPage(in.ProjectID, in.Cursor, in.Limit, 50)
	if e != nil {
		return nil, nil, e
	}
	if in.State != "" {
		q.Set("state", in.State)
	}
	return rt.releaseToolRead(ctx, releasePath(in.ProjectID, "")+"?"+q.Encode())
}
func (rt *runtime) toolReleaseGet(ctx context.Context, _ *mcp.CallToolRequest, in releaseGetArgs) (*mcp.CallToolResult, any, error) {
	if !validUUID(in.ProjectID) || !validUUID(in.ReleaseID) {
		return nil, nil, errors.New("project and release UUIDs required")
	}
	return rt.releaseToolRead(ctx, releasePath(in.ProjectID, in.ReleaseID))
}
func (rt *runtime) toolReleaseItems(ctx context.Context, _ *mcp.CallToolRequest, in releaseItemsArgs) (*mcp.CallToolResult, any, error) {
	q, e := toolPage(in.ProjectID, in.Cursor, in.Limit, 200)
	if e != nil || !validUUID(in.ReleaseID) {
		return nil, nil, errors.New("invalid item page identity")
	}
	if in.Through != "" {
		if !validUUID(in.Through) || in.CompletedLater {
			return nil, nil, errors.New("invalid through scope")
		}
		q.Set("through", in.Through)
	}
	if in.CompletedLater {
		q.Set("completed_later", "1")
	}
	return rt.releaseToolRead(ctx, releasePath(in.ProjectID, in.ReleaseID)+"/items?"+q.Encode())
}
func (rt *runtime) toolBacklogList(ctx context.Context, _ *mcp.CallToolRequest, in backlogListArgs) (*mcp.CallToolResult, any, error) {
	q, e := toolPage(in.ProjectID, in.Cursor, in.Limit, 200)
	if e != nil {
		return nil, nil, e
	}
	if in.Part != "" {
		if in.Part != "ranked" && in.Part != "tail" {
			return nil, nil, errors.New("invalid backlog part")
		}
		q.Set("part", in.Part)
	}
	if in.CompletedUnplaced {
		q.Set("completed_unplaced", "1")
	}
	return rt.releaseToolRead(ctx, "/api/projects/"+in.ProjectID+"/backlog?"+q.Encode())
}
func (rt *runtime) toolShipsInPlace(ctx context.Context, _ *mcp.CallToolRequest, in shipsInPlaceArgs) (*mcp.CallToolResult, any, error) {
	if !validUUID(in.ItemID) || !validUUID(in.ProjectID) || in.ExpectedRevision < 0 || in.ReleaseID != nil && (!validUUID(*in.ReleaseID) || in.ExpectedReleaseRevision < 1) || in.BeforeID != "" && !validUUID(in.BeforeID) || in.AfterID != "" && !validUUID(in.AfterID) {
		return nil, nil, errors.New("invalid placement identity or revision")
	}
	body := map[string]any{"expected_project_id": in.ProjectID, "expected_revision": in.ExpectedRevision, "release_id": in.ReleaseID}
	if in.ExpectedReleaseRevision > 0 {
		body["expected_release_revision"] = in.ExpectedReleaseRevision
	}
	if in.BeforeID != "" {
		body["before_id"] = in.BeforeID
	}
	if in.AfterID != "" {
		body["after_id"] = in.AfterID
	}
	var raw json.RawMessage
	if e := rt.doCtx(ctx, http.MethodPut, "/api/nodes/"+in.ItemID+"/ships-in", body, &raw); e != nil {
		return nil, nil, e
	}
	var out any
	e := json.Unmarshal(raw, &out)
	return nil, out, e
}

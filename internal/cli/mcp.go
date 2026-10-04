// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/version"
)

func (rt *runtime) cmdMCP() *Command {
	return &Command{
		Name:  "mcp",
		Short: "MCP server over stdio",
		Long:  "Exposes whoami, ask, ask_status and the issue, knowledge and search tools. Uses the same scoped API operations as the CLI.",
		Use:   "mcp",
		run: func(args []string) error {
			ctx, stop := signalContext()
			defer stop()
			return rt.mcpServer().Run(ctx, &mcp.StdioTransport{})
		},
	}
}

func (rt *runtime) mcpServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: rt.program, Version: version.Version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "whoami",
		Description: "Return the acting principal, tenant and identity (GET /api/me).",
	}, rt.toolWhoami)
	mcp.AddTool(s, &mcp.Tool{Name: "ask", Description: "Leave a durable question for a person; return immediately with ID, state and destination. Same scoped contract as HTTP and CLI; request_id is required for retries."}, rt.toolAsk)
	mcp.AddTool(s, &mcp.Tool{Name: "ask_status", Description: "Read your authorized question, answer revision and delivery state without waiting."}, rt.toolAskStatus)
	addWorkTool(s, rt, "issue_list", "List issues.", issueListArgs{})
	addWorkTool(s, rt, "issue_get", "Fetch one issue by key.", issueRefArgs{})
	addWorkTool(s, rt, "issue_create", "Create an issue.", issueCreateArgs{})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "issue_update",
		Description: "Update an issue. A type or kind is refused with kind_change_not_allowed; updates use the node revision precondition.",
	}, rt.toolIssueUpdate)
	addWorkTool(s, rt, "issue_comment", "Comment on an issue.", issueCommentArgs{})
	addWorkTool(s, rt, "knowledge_list", "List knowledge entries.", knowledgeListArgs{})
	addWorkTool(s, rt, "knowledge_get", "Fetch one knowledge entry.", knowledgeGetArgs{})
	addWorkTool(s, rt, "knowledge_create", "Create a knowledge entry.", knowledgeCreateArgs{})
	addWorkTool(s, rt, "knowledge_update", "Update a knowledge entry.", knowledgeUpdateArgs{})
	addWorkTool(s, rt, "search", "Search issues by free text.", searchArgs{})
	return s
}

type noArgs struct{}

type issueListArgs struct {
	Project  string `json:"project,omitempty" jsonschema:"project key"`
	Status   string `json:"status,omitempty" jsonschema:"status filter"`
	Type     string `json:"type,omitempty" jsonschema:"work; epic ticket and task are compatibility aliases after migration"`
	Limit    int    `json:"limit,omitempty" jsonschema:"page size"`
	Offset   int    `json:"offset,omitempty" jsonschema:"pagination offset"`
	Priority string `json:"priority,omitempty" jsonschema:"priority filter"`
}

type issueRefArgs struct {
	Ref string `json:"ref" jsonschema:"issue key such as AEON-18, or id:<n>"`
}

type issueCreateArgs struct {
	Project     string   `json:"project" jsonschema:"project key"`
	Title       string   `json:"title" jsonschema:"issue title"`
	Parent      string   `json:"parent,omitempty" jsonschema:"parent work item key; depth decides the name"`
	Type        string   `json:"type,omitempty" jsonschema:"work; epic ticket and task are compatibility aliases after migration"`
	Status      string   `json:"status,omitempty" jsonschema:"initial status"`
	Description string   `json:"description,omitempty" jsonschema:"description markdown"`
	Tags        []string `json:"tags,omitempty" jsonschema:"tag names preserved when filing the issue"`
	Bug         bool     `json:"bug,omitempty" jsonschema:"mark a repair as a fix in release notes by adding the bug tag while preserving the issue type"`
}

type issueUpdateArgs struct {
	Ref         string `json:"ref" jsonschema:"issue key or id:<n>"`
	Title       string `json:"title,omitempty" jsonschema:"new title"`
	Status      string `json:"status,omitempty" jsonschema:"new status"`
	Description string `json:"description,omitempty" jsonschema:"new description markdown"`
	Type        string `json:"type,omitempty" jsonschema:"issue kind slug; a change is refused"`
	Kind        string `json:"kind,omitempty" jsonschema:"issue kind slug; a change is refused"`
}

func (rt *runtime) toolIssueUpdate(ctx context.Context, _ *mcp.CallToolRequest, in issueUpdateArgs) (*mcp.CallToolResult, any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	requested := strings.TrimSpace(in.Kind)
	if typ := strings.TrimSpace(in.Type); typ != "" {
		if requested != "" && requested != typ {
			return nil, nil, errors.New("kind and type disagree")
		}
		requested = typ
	}
	work := rt.workRuntime(ctx)
	if requested != "" {
		if err := work.checkIssueKind(in.Ref, requested); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(in.Title+in.Status) == "" && in.Description == "" {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "kind is already " + requested}}}, nil, nil
		}
	}
	result, err := work.updateIssueResult(issuePatch{Ref: in.Ref, Title: in.Title, Status: in.Status, Description: in.Description})
	return nil, result.Issue, err
}

type issueCommentArgs struct {
	Ref  string `json:"ref" jsonschema:"issue key or id:<n>"`
	Body string `json:"body" jsonschema:"comment markdown"`
}

type knowledgeListArgs struct {
	Project string `json:"project" jsonschema:"project key"`
	Type    string `json:"type,omitempty" jsonschema:"knowledge type"`
}

type knowledgeGetArgs struct {
	Project string `json:"project" jsonschema:"project key"`
	Type    string `json:"type" jsonschema:"knowledge type"`
	Slug    string `json:"slug" jsonschema:"entry slug"`
}

type knowledgeCreateArgs struct {
	Project string `json:"project" jsonschema:"project key"`
	Type    string `json:"type" jsonschema:"knowledge type"`
	Slug    string `json:"slug" jsonschema:"entry slug"`
	Title   string `json:"title" jsonschema:"title"`
	Body    string `json:"body,omitempty" jsonschema:"markdown body"`
}

type knowledgeUpdateArgs struct {
	Project string `json:"project" jsonschema:"project key"`
	Type    string `json:"type" jsonschema:"knowledge type"`
	Slug    string `json:"slug" jsonschema:"entry slug"`
	Title   string `json:"title,omitempty" jsonschema:"new title"`
	Body    string `json:"body,omitempty" jsonschema:"replacement markdown body"`
	Status  string `json:"status,omitempty" jsonschema:"new status"`
}

type searchArgs struct {
	Query   string `json:"query" jsonschema:"free-text query"`
	Project string `json:"project,omitempty" jsonschema:"project key"`
	Type    string `json:"type,omitempty" jsonschema:"work; epic ticket and task are compatibility aliases after migration"`
	Limit   int    `json:"limit,omitempty" jsonschema:"page size"`
}

func (rt *runtime) toolWhoami(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, client.Me, error) {
	inst, err := rt.resolve()
	if err != nil {
		return nil, client.Me{}, err
	}
	me, err := client.New(inst.URL, inst.APIKey).Me(ctx)
	if err != nil {
		return nil, client.Me{}, errors.New(redact(err.Error(), inst.APIKey))
	}
	return nil, me, nil
}

// addWorkTool adapts reusable application operations to MCP without invoking
// command parsing or capturing terminal output. Each call owns its cache/context.
func addWorkTool[In any](s *mcp.Server, rt *runtime, name, description string, _ In) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		work := rt.workRuntime(ctx)
		var result any
		var err error
		switch args := any(in).(type) {
		case issueListArgs:
			if args.Limit < 0 || args.Limit > 200 || args.Offset < 0 || args.Offset > 10000 {
				return nil, nil, usagef("limit 0–200 and offset 0–10000 required")
			}
			result, err = work.listIssuesResult(args.Project, args.Status, args.Type, args.Priority, "", args.Limit, args.Offset)
		case issueRefArgs:
			result, err = work.getIssueResult(args.Ref)
		case issueCreateArgs:
			tags := append([]string(nil), args.Tags...)
			if args.Bug && !slices.Contains(tags, "bug") {
				tags = append(tags, "bug")
			}
			result, err = work.createIssueResult(issueInput{Project: args.Project, Title: args.Title, Type: args.Type, Status: args.Status, Parent: args.Parent, Description: args.Description, Tags: tags})
		case issueCommentArgs:
			var comment issueCommentResult
			comment, err = work.commentIssueResult(args.Ref, args.Body)
			result = comment.Comment
		case knowledgeListArgs:
			var kinds kindTable
			var nodes []apiNode
			kinds, nodes, err = work.knowledgeNodes(args.Project, args.Type)
			items := []knowledgeView{}
			for _, n := range nodes {
				items = append(items, viewKnowledge(n, kinds))
			}
			result = items
		case knowledgeGetArgs:
			var kinds kindTable
			var nodes []apiNode
			kinds, nodes, err = work.knowledgeNodes(args.Project, args.Type)
			if err == nil {
				err = fmt.Errorf("knowledge %s/%s not found", args.Type, args.Slug)
				for _, n := range nodes {
					if fieldString(fieldMap(n.Fields), "slug") == args.Slug {
						result = viewKnowledge(n, kinds)
						err = nil
						break
					}
				}
			}
		case knowledgeCreateArgs:
			result, err = work.createKnowledgeResult(args.Project, args.Type, args.Slug, args.Title, args.Body, "")
		case knowledgeUpdateArgs:
			result, err = work.updateKnowledgeResult(args.Type, args.Slug, args.Project, args.Title, args.Body, args.Status, "", "")
		case searchArgs:
			result, err = work.searchIssuesResult(args.Query, args.Project, args.Type, args.Limit)
		default:
			err = fmt.Errorf("unsupported work tool")
		}
		return nil, result, err
	})
}

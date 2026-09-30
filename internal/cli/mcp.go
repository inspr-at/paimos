// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/version"
)

func (rt *runtime) cmdMCP() *Command {
	return &Command{
		Name:  "mcp",
		Short: "MCP server over stdio",
		Long:  "Exposes whoami and the issue, knowledge and search tools. Unimplemented tools report that they arrive in R1.",
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
	addR1Tool(s, "issue_list", "List issues.", issueListArgs{})
	addR1Tool(s, "issue_get", "Fetch one issue by key.", issueRefArgs{})
	addR1Tool(s, "issue_create", "Create an issue.", issueCreateArgs{})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "issue_update",
		Description: "Update an issue. A type or kind is refused with kind_change_not_allowed; other updates arrive in R1.",
	}, rt.toolIssueUpdate)
	addR1Tool(s, "issue_comment", "Comment on an issue.", issueCommentArgs{})
	addR1Tool(s, "knowledge_list", "List knowledge entries.", knowledgeListArgs{})
	addR1Tool(s, "knowledge_get", "Fetch one knowledge entry.", knowledgeGetArgs{})
	addR1Tool(s, "knowledge_create", "Create a knowledge entry.", knowledgeCreateArgs{})
	addR1Tool(s, "knowledge_update", "Update a knowledge entry.", knowledgeUpdateArgs{})
	addR1Tool(s, "search", "Search issues by free text.", searchArgs{})
	return s
}

type noArgs struct{}

type issueListArgs struct {
	Project  string `json:"project,omitempty" jsonschema:"project key"`
	Status   string `json:"status,omitempty" jsonschema:"status filter"`
	Type     string `json:"type,omitempty" jsonschema:"issue type"`
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
	Type        string   `json:"type,omitempty" jsonschema:"issue type"`
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
	if requested == "" {
		return nil, nil, errors.New("issue_update arrives in R1")
	}
	n, err := rt.nodeByKey(strings.TrimSpace(in.Ref))
	if err != nil {
		return nil, nil, err
	}
	kinds, err := rt.loadKinds()
	if err != nil {
		return nil, nil, err
	}
	current := kinds.slug(n.KindID)
	if current == requested {
		if strings.TrimSpace(in.Title) != "" || strings.TrimSpace(in.Status) != "" || strings.TrimSpace(in.Description) != "" {
			return nil, nil, errors.New("issue_update arrives in R1")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "kind is already " + current}}}, nil, nil
	}
	return nil, nil, errors.New("kind_change_not_allowed")
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
	Type    string `json:"type,omitempty" jsonschema:"issue type"`
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

func addR1Tool[In any](s *mcp.Server, name, description string, _ In) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        name,
		Description: description + " Not in the Aeon API yet.",
	}, func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New(name + " arrives in R1")
	})
}

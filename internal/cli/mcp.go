// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
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
		Long:  "Exposes whoami, ask, ask_status and the issue, knowledge and search tools. Unimplemented tools report that they arrive in R1.",
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
	mcp.AddTool(s, &mcp.Tool{Name: "issue_list", Description: "List work items. work, epic, ticket and task alias the work kind after migration; names follow nesting."}, rt.toolIssueList)
	mcp.AddTool(s, &mcp.Tool{Name: "issue_get", Description: "Read a work item including is_leaf, depth and level_name."}, rt.toolIssueGet)
	mcp.AddTool(s, &mcp.Tool{Name: "issue_create", Description: "Create a work item; nesting decides its name. epic, ticket and task are compatibility aliases on migrated workspaces."}, rt.toolIssueCreate)
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
	if k, ok := kinds.issueKind(requested); ok && k.ID == n.KindID {
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

func addR1Tool[In any](s *mcp.Server, name, description string, _ In) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        name,
		Description: description + " Not in the Aeon API yet.",
	}, func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New(name + " arrives in R1")
	})
}

// A request-local runtime keeps MCP calls from racing on shared output sinks.
// These tools use the same authorization and compatibility paths as the CLI.
func (rt *runtime) issueTool(run func(*runtime) error) (*mcp.CallToolResult, any, error) {
	copy := *rt
	var out bytes.Buffer
	copy.stdout = &out
	copy.stderr = &out
	copy.jsonOut = true
	if err := run(&copy); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.String()}}}, nil, nil
}
func (rt *runtime) toolIssueList(ctx context.Context, req *mcp.CallToolRequest, in issueListArgs) (*mcp.CallToolResult, any, error) {
	return rt.issueTool(func(r *runtime) error {
		return r.listIssues(in.Project, in.Status, in.Type, in.Priority, "", in.Limit, in.Offset)
	})
}
func (rt *runtime) toolIssueGet(ctx context.Context, req *mcp.CallToolRequest, in issueRefArgs) (*mcp.CallToolResult, any, error) {
	return rt.issueTool(func(r *runtime) error { return r.getIssue(in.Ref) })
}
func (rt *runtime) toolIssueCreate(ctx context.Context, req *mcp.CallToolRequest, in issueCreateArgs) (*mcp.CallToolResult, any, error) {
	tags := append([]string(nil), in.Tags...)
	if in.Bug {
		tags = append(tags, "bug")
	}
	return rt.issueTool(func(r *runtime) error {
		return r.createIssue(issueInput{Project: in.Project, Title: in.Title, Type: in.Type, Status: in.Status, Parent: in.Parent, Description: in.Description, Tags: tags})
	})
}

// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/questions"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (rt *runtime) cmdAsk() *Command {
	var project, ticket, contextFile string
	var options []string
	in := questions.Input{Meanwhile: "carries_on"}
	return &Command{Name: "ask", Short: "Leave a durable question for a person", Use: `ask --project KEY [--ticket KEY] QUESTION --option '["Title","Description","Answer"]'`, minArgs: 1, maxArgs: 1,
		Long: "Returns immediately with a durable question ID and comment destination. Repeat --option with JSON triples or objects {id,title,description,answer}. Triple IDs are 1 through 9. Retain --request-id for retries. No terminal question and no permission escalation.",
		subs: []*Command{{Name: "status", Short: "Read a question and its delivery state", Use: "ask status <question-uuid>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
			q, err := rt.questionStatus(context.Background(), args[0])
			if err != nil {
				return err
			}
			return rt.printQuestion(q)
		}}},
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key or UUID (required)")
			fs.string(&ticket, "ticket", 0, "optional ticket key or UUID")
			fs.string(&contextFile, "context-file", 0, "UTF-8 context file (at most 16000 bytes; - reads stdin)")
			fs.strings(&options, "option", "JSON [title,description,answer] or {id,title,description,answer}; repeat up to 9 times")
			fs.string(&in.RequestID, "request-id", 0, "stable request UUID for retries (generated and printed if omitted)")
			fs.string(&in.SessionID, "session", 0, "exact original public Aeon session UUID; defaults to the current verified binding")
			fs.string(&in.Findings, "findings", 0, "what the agent found")
			fs.string(&in.Recommend, "recommend", 0, "recommended option ID")
			fs.string(&in.Why, "why", 0, "reason for the recommendation")
			fs.string(&in.Meanwhile, "meanwhile", 0, "carries_on, parked, paused or stopped")
			fs.string(&in.MeanwhileText, "meanwhile-text", 0, "what happens while the question waits")
			fs.strings(&in.BlockedNodeIDs, "blocked-node", "explicit blocked work node UUID; repeat up to 20")
			fs.string(&in.SuggestedOutcome, "keep", 0, "suggested outcome: once, always, requirement or doctrine")
			fs.string(&in.AnywayReason, "anyway", 0, "open a fresh question instead of future matching; record why")
			fs.string(&in.SourceRequestID, "source-request", 0, "owned inbox source message UUID")
		}, run: func(args []string) error {
			in.Question = args[0]
			for i, raw := range options {
				o, err := parseQuestionOption(raw, i+1)
				if err != nil {
					return usagef("%s", err.Error())
				}
				in.Options = append(in.Options, o)
			}
			if contextFile != "" {
				var reader io.Reader = rt.stdin
				if contextFile != "-" {
					f, err := os.Open(contextFile)
					if err != nil {
						return rt.fail(err, "")
					}
					defer f.Close()
					reader = f
				}
				b, err := io.ReadAll(io.LimitReader(reader, 16001))
				if err != nil {
					return rt.fail(err, "")
				}
				if len(b) > 16000 {
					return usagef("context exceeds 16000 bytes")
				}
				in.Context = string(b)
			}
			if in.RequestID == "" {
				var err error
				in.RequestID, err = newUUIDv4()
				if err != nil {
					return err
				}
			}
			// Print before the request so even a lost response can be retried exactly.
			fmt.Fprintln(rt.stderr, "request_id:", in.RequestID)
			q, err := rt.sendQuestion(context.Background(), project, ticket, in)
			if err != nil {
				return err
			}
			return rt.printQuestion(q)
		}}
}
func parseQuestionOption(raw string, n int) (questions.Option, error) {
	var o questions.Option
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		var triple []string
		if json.Unmarshal([]byte(raw), &triple) != nil || len(triple) != 3 {
			return o, fmt.Errorf("option must be a JSON triple or object")
		}
		return questions.Option{ID: fmt.Sprint(n), Title: triple[0], Description: triple[1], Answer: triple[2]}, nil
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&o); err != nil {
		return o, fmt.Errorf("option must be a JSON triple or object")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return o, fmt.Errorf("option must contain one JSON object")
	}
	return o, nil
}
func (rt *runtime) sendQuestion(ctx context.Context, project, ticket string, in questions.Input) (questions.Question, error) {
	var out questions.Question
	if strings.TrimSpace(project) == "" {
		return out, usagef("--project is required")
	}
	// UUID addressing requires no broad nodes.read key scope. Named addressing
	// deliberately reuses the existing scoped node/key resolver.
	projectID := project
	if !validUUID(project) {
		p, err := rt.projectNodeCtx(ctx, project)
		if err != nil {
			return out, err
		}
		projectID = p.ID
	}
	if ticket != "" {
		if in.TicketID != "" {
			return out, usagef("specify ticket or input.ticket_id, not both")
		}
		in.TicketID = ticket
		if !validUUID(ticket) {
			var node apiNode
			if err := rt.doCtx(ctx, http.MethodGet, "/api/node-keys/"+url.PathEscape(ticket), nil, &node); err != nil {
				return out, err
			}
			in.TicketID = node.ID
		}
	}
	if in.SessionID == "" {
		me, err := rt.caller()
		if err != nil {
			return out, err
		}
		in.SessionID, err = rt.ambientSenderSession(ctx, projectID, me.Principal.ID)
		if err != nil {
			return out, err
		}
	}
	if err := in.Validate(); err != nil {
		return out, usagef("%s", err.Error())
	}
	body, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	if len(body) > questions.MaxBody {
		return out, usagef("question exceeds 64 KiB")
	}
	err = rt.doCtx(ctx, http.MethodPost, "/api/projects/"+url.PathEscape(projectID)+"/questions", in, &out)
	return out, err
}
func (rt *runtime) questionStatus(ctx context.Context, id string) (questions.Question, error) {
	var out questions.Question
	if !validUUID(id) {
		return out, usagef("question ID must be a UUID")
	}
	err := rt.doCtx(ctx, http.MethodGet, "/api/questions/"+url.PathEscape(id)+"/status", nil, &out)
	return out, err
}
func (rt *runtime) printQuestion(q questions.Question) error {
	if rt.jsonOut {
		return rt.printJSON(q)
	}
	fmt.Fprintf(rt.stdout, "question: %s\nstate: %s\nrevision: %d\n", q.ID, q.State, q.Revision)
	for _, a := range q.Askers {
		fmt.Fprintf(rt.stdout, "comment destination: %s\nreply root: %s\n", a.CommentNodeID, a.ReplyRootID)
	}
	if q.Answer != nil {
		fmt.Fprintln(rt.stdout, "answer:", q.Answer.Answer)
	}
	for _, p := range q.Pending {
		fmt.Fprintf(rt.stdout, "%s: %s\n", p.Kind, p.State)
	}
	return nil
}

type askToolArgs struct {
	Project string          `json:"project" jsonschema:"project key or UUID"`
	Ticket  string          `json:"ticket,omitempty" jsonschema:"optional ticket key or UUID"`
	Input   questions.Input `json:"input" jsonschema:"same typed input as HTTP; retain request_id for retries"`
}
type askStatusToolArgs struct {
	QuestionID string `json:"question_id" jsonschema:"durable question UUID"`
}

func (rt *runtime) toolAsk(ctx context.Context, _ *mcp.CallToolRequest, in askToolArgs) (*mcp.CallToolResult, any, error) {
	q, err := rt.sendQuestion(ctx, in.Project, in.Ticket, in.Input)
	if err != nil {
		return nil, nil, err
	}
	return questionToolResult(q)
}
func (rt *runtime) toolAskStatus(ctx context.Context, _ *mcp.CallToolRequest, in askStatusToolArgs) (*mcp.CallToolResult, any, error) {
	q, err := rt.questionStatus(ctx, in.QuestionID)
	if err != nil {
		return nil, nil, err
	}
	return questionToolResult(q)
}
func questionToolResult(q questions.Question) (*mcp.CallToolResult, any, error) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(q); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}, q, nil
}

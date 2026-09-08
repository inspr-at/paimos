// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type cursorDecisionWriteCloser struct{ bytes.Buffer }

func (*cursorDecisionWriteCloser) Close() error { return nil }

func TestParseCursorPermissionInstalledShapes(t *testing.T) {
	fixtures := []struct {
		name            string
		kind            string
		title           string
		rawInput        any
		content         any
		contentFragment string
	}{
		{name: "documented raw input", kind: "read", title: "Read file", rawInput: map[string]any{"path": "fixture.txt"}},
		{name: "installed title only", kind: "fetch", title: "Fetch documentation"},
		{name: "installed shell text", kind: "execute", title: "`go test ./agentd`", content: []any{
			map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Run focused tests"}},
		}, contentFragment: "Run focused tests"},
		{name: "installed write diff", kind: "edit", title: "Edit `fixture.txt`", content: []any{
			map[string]any{"type": "diff", "path": "fixture.txt", "oldText": "before", "newText": "after"},
		}, contentFragment: `"type":"diff"`},
		{name: "installed new file diff", kind: "edit", title: "Write fixture.txt", content: []any{
			map[string]any{"type": "diff", "path": "fixture.txt", "oldText": nil, "newText": "created"},
		}, contentFragment: `"oldText":null`},
		{name: "installed mcp text", kind: "other", title: "Fixture: inspect", content: []any{
			map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "{\"limit\":1}"}},
		}, contentFragment: `\"limit\":1`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			held, diagnostic, ok := parseCursorPermissionDetailed(cursorPermissionTestMessage(t, cursorPermissionTestParams(fixture.kind, fixture.title, fixture.rawInput, fixture.content)), "sess-owned", "gen-owned")
			if !ok || diagnostic != nil || held == nil {
				t.Fatalf("ok=%v diagnostic=%+v held=%+v", ok, diagnostic, held)
			}
			if held.inspect.Incomplete || !held.inspect.Untrusted || held.public.Digest == "" {
				t.Fatalf("inspect=%+v public=%+v", held.inspect, held.public)
			}
			if fixture.rawInput != nil && held.inspect.ToolInput == "" {
				t.Fatal("raw input was omitted from local inspection")
			}
			if fixture.content != nil && !strings.Contains(held.inspect.ToolContent, fixture.contentFragment) {
				t.Fatalf("tool content=%q", held.inspect.ToolContent)
			}
			public, err := json.Marshal(held.public)
			if err != nil {
				t.Fatal(err)
			}
			secrets := []string{fixture.title, "fixture.txt"}
			if fixture.contentFragment != "" {
				secrets = append(secrets, fixture.contentFragment)
			}
			assertNoCursorPublicContent(t, public, secrets...)
		})
	}
}

func TestCursorPermissionContentChangesDigest(t *testing.T) {
	permission := func(newText string) *cursorHeldDecision {
		params := cursorPermissionTestParams("edit", "Edit fixture", nil, []any{
			map[string]any{"type": "diff", "path": "fixture.txt", "oldText": "before", "newText": newText},
		})
		held, diagnostic, ok := parseCursorPermissionDetailed(cursorPermissionTestMessage(t, params), "sess-owned", "gen-owned")
		if !ok || diagnostic != nil {
			t.Fatalf("ok=%v diagnostic=%+v", ok, diagnostic)
		}
		return held
	}
	first := permission("after-one")
	second := permission("after-two")
	if first.public.Digest == second.public.Digest {
		t.Fatal("diff content change did not alter the decision digest")
	}
}

func TestCursorPermissionIncompleteContentCannotApprove(t *testing.T) {
	fixtures := []struct {
		name    string
		content any
	}{
		{name: "unknown block", content: []any{map[string]any{"type": "terminal", "terminalId": "local"}}},
		{name: "truncated text", content: []any{map[string]any{
			"type": "content", "content": map[string]any{"type": "text", "text": strings.Repeat("x", maxCursorInspectBytes+64)},
		}}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			held, diagnostic, ok := parseCursorPermissionDetailed(cursorPermissionTestMessage(t, cursorPermissionTestParams("other", "Inspect operation", nil, fixture.content)), "sess-owned", "gen-owned")
			if !ok || diagnostic != nil || held == nil || !held.inspect.Incomplete || held.inspect.ToolContent == "" {
				t.Fatalf("ok=%v diagnostic=%+v held=%+v", ok, diagnostic, held)
			}
			held.public.ExpiresAt = time.Now().Add(time.Minute)
			writer := &cursorDecisionWriteCloser{}
			process := &cursorProcess{
				generation: "gen-owned", sessionID: "sess-owned", stdin: writer,
				held: map[string]*cursorHeldDecision{held.public.RequestID: held}, evidence: &cursorEvidenceStore{},
			}
			answer := DecisionAnswer{
				RequestID: held.public.RequestID, Generation: "gen-owned", Digest: held.public.Digest,
				OptionID: "allow-once", Authority: DecisionAuthorityLocalOperator,
			}
			if _, err := process.Answer(context.Background(), answer); !errors.Is(err, ErrDecisionIncomplete) {
				t.Fatalf("incomplete approval err=%v", err)
			}
			if len(process.PendingDecisions()) != 1 {
				t.Fatal("failed approval consumed the pending request")
			}
			answer.OptionID = "reject-once"
			if _, err := process.Answer(context.Background(), answer); err != nil {
				t.Fatalf("explicit rejection err=%v", err)
			}
			if len(process.PendingDecisions()) != 0 || !strings.Contains(writer.String(), `"optionId":"reject-once"`) {
				t.Fatalf("pending=%+v response=%q", process.PendingDecisions(), writer.String())
			}
		})
	}
}

func TestCursorPermissionMissingAndNullContentHandling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setContent bool
		incomplete bool
		shape      string
	}{
		{name: "missing", incomplete: false, shape: "absent"},
		{name: "null", setContent: true, incomplete: true, shape: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := cursorPermissionTestParams("execute", "Run focused command", map[string]any{"command": "go test ./agentd"}, nil)
			if tc.setContent {
				params["toolCall"].(map[string]any)["content"] = nil
			}
			held, diagnostic, ok := parseCursorPermissionDetailed(cursorPermissionTestMessage(t, params), "sess-owned", "gen-owned")
			if !ok || diagnostic != nil || held == nil {
				t.Fatalf("ok=%v diagnostic=%+v held=%+v", ok, diagnostic, held)
			}
			if held.inspect.Incomplete != tc.incomplete || held.inspect.ToolContent != "" {
				t.Fatalf("inspect=%+v", held.inspect)
			}
			if shape := cursorPermissionShape(cursorPermissionTestMessage(t, params).Params); shape.ContentType != tc.shape {
				t.Fatalf("content shape=%q, want %q", shape.ContentType, tc.shape)
			}

			if tc.incomplete {
				held.public.ExpiresAt = time.Now().Add(time.Minute)
				writer := &cursorDecisionWriteCloser{}
				process := &cursorProcess{
					generation: "gen-owned", sessionID: "sess-owned", stdin: writer,
					held: map[string]*cursorHeldDecision{held.public.RequestID: held}, evidence: &cursorEvidenceStore{},
				}
				answer := DecisionAnswer{
					RequestID: held.public.RequestID, Generation: "gen-owned", Digest: held.public.Digest,
					OptionID: "allow-once", Authority: DecisionAuthorityLocalOperator,
				}
				if _, err := process.Answer(context.Background(), answer); !errors.Is(err, ErrDecisionIncomplete) {
					t.Fatalf("null-content approval err=%v", err)
				}
				if len(process.PendingDecisions()) != 1 {
					t.Fatal("failed approval consumed the null-content request")
				}
				answer.OptionID = "reject-once"
				if _, err := process.Answer(context.Background(), answer); err != nil {
					t.Fatalf("null-content rejection err=%v", err)
				}
				if len(process.PendingDecisions()) != 0 || !strings.Contains(writer.String(), `"optionId":"reject-once"`) {
					t.Fatalf("pending=%+v response=%q", process.PendingDecisions(), writer.String())
				}
			}
		})
	}
}

func TestCursorPermissionMalformedDiagnosticsAreValueFree(t *testing.T) {
	fixtures := []struct {
		name       string
		params     map[string]any
		code       string
		shapeField func(*DecisionRequestShape) string
		shapeValue string
	}{
		{
			name: "missing tool call", params: map[string]any{"sessionId": "sess-owned", "options": cursorPermissionTestOptions()},
			code: "permission_tool_call_missing", shapeField: func(shape *DecisionRequestShape) string { return shape.ToolCallType }, shapeValue: "absent",
		},
		{
			name: "malformed session", params: map[string]any{"sessionId": 7, "toolCall": map[string]any{"toolCallId": "call", "title": "secret-command"}, "options": cursorPermissionTestOptions()},
			code: "permission_params_invalid", shapeField: func(shape *DecisionRequestShape) string { return shape.SessionIDType }, shapeValue: "number",
		},
		{
			name: "malformed content", params: cursorPermissionTestParams("edit", "secret-command", nil, []any{
				map[string]any{"type": "diff", "path": "secret-path", "newText": "secret-content"},
			}),
			code: "permission_content_invalid", shapeField: func(shape *DecisionRequestShape) string { return shape.ContentType }, shapeValue: "array",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			_, diagnostic, ok := parseCursorPermissionDetailed(cursorPermissionTestMessage(t, fixture.params), "sess-owned", "gen-owned")
			if ok || diagnostic == nil || diagnostic.code != fixture.code || diagnostic.structure == nil {
				t.Fatalf("ok=%v diagnostic=%+v", ok, diagnostic)
			}
			if got := fixture.shapeField(diagnostic.structure); got != fixture.shapeValue {
				t.Fatalf("shape value=%q want=%q shape=%+v", got, fixture.shapeValue, diagnostic.structure)
			}
			process := &cursorProcess{}
			process.noteRefusalDiagnostic("session/request_permission", "invalid", diagnostic)
			encoded, err := json.Marshal(process.DecisionRefusals())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(encoded, []byte(fixture.code)) {
				t.Fatalf("diagnostic code missing: %s", encoded)
			}
			assertNoCursorPublicContent(t, encoded, "secret-command", "secret-path", "secret-content", "sess-owned")
		})
	}
}

func cursorPermissionTestMessage(t *testing.T, params map[string]any) cursorRPCMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return cursorRPCMessage{ID: json.RawMessage(`"permission-1"`), Method: "session/request_permission", Params: raw}
}

func cursorPermissionTestParams(kind, title string, rawInput, content any) map[string]any {
	toolCall := map[string]any{
		"toolCallId": "call-owned", "kind": kind, "title": title, "status": "pending",
	}
	if rawInput != nil {
		toolCall["rawInput"] = rawInput
	}
	if content != nil {
		toolCall["content"] = content
	}
	return map[string]any{"sessionId": "sess-owned", "toolCall": toolCall, "options": cursorPermissionTestOptions()}
}

func cursorPermissionTestOptions() []map[string]any {
	return []map[string]any{
		{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
		{"optionId": "allow-always", "name": "Allow always", "kind": "allow_always"},
		{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Risk: native message, tool and lifecycle schemas drift apart, or a vendor
// payload/unknown frame accidentally becomes durable run telemetry.
func TestChatHarnessFixtureParity(t *testing.T) {
	want := []ChatUpdate{chatState("running"), chatText("Hello "), chatText("world"), chatTool("call-1", "Bash", "in_progress"), chatState("requires_action"), chatTool("call-1", "Bash", "completed"), chatState("idle")}
	for _, harness := range []string{Claude, Codex, Pi} {
		t.Run(harness, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "chat-"+harness+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var got []ChatUpdate
			p := &wireProcess{threadID: "thread-1", turnID: "turn-1", observe: func(ev AdapterEvent) {
				if ev.Chat == nil || ev.Kind != "" {
					t.Fatal("chat mixed with durable telemetry")
				}
				got = append(got, *ev.Chat)
			}}
			lines := bufio.NewScanner(bytes.NewReader(raw))
			for lines.Scan() {
				p.observeChat(harness, lines.Bytes())
			}
			if err := lines.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("normalized updates: %#v", got)
			}
			if p.DroppedChatFrames() != 1 {
				t.Fatalf("unknown frame count %d", p.DroppedChatFrames())
			}
			encoded, _ := json.Marshal(got)
			if bytes.Contains(encoded, []byte("PRIVATE_SENTINEL")) {
				t.Fatal("raw vendor content leaked")
			}
		})
	}
}

func TestChatCapabilitiesHonorOwnedSessionControls(t *testing.T) {
	for _, tc := range []struct {
		harness string
		caps    []string
		want    ChatCapabilities
	}{
		{Claude, []string{"steer", "interrupt"}, ChatCapabilities{"native", true, true}},
		{Codex, []string{"steer", "interrupt"}, ChatCapabilities{"native", true, true}},
		{Pi, []string{"steer", "interrupt"}, ChatCapabilities{"next-step", true, true}},
		{Claude, []string{"inbox"}, ChatCapabilities{"queue", false, true}},
		{Grok, []string{"steer", "interrupt"}, ChatCapabilities{"queue", false, false}},
		{Gemini, []string{"steer", "interrupt"}, ChatCapabilities{"queue", false, false}},
	} {
		if got := sessionChatCapabilities(tc.harness, tc.caps); got != tc.want {
			t.Fatalf("%s: %#v", tc.harness, got)
		}
	}
}

func TestChatRejectsForeignAndOversizedFrames(t *testing.T) {
	var got []ChatUpdate
	p := &wireProcess{threadID: "thread-1", turnID: "turn-1", observe: func(ev AdapterEvent) { got = append(got, *ev.Chat) }}
	fixture := `{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","delta":"safe"}}`
	for _, raw := range []string{strings.Replace(fixture, "thread-1", "foreign", 1), strings.Replace(fixture, "turn-1", "old-turn", 1), `{"method":42}`, `{`, strings.Repeat(" ", (8<<20)+1), strings.Replace(fixture, "safe", strings.Repeat("x", chatChunkBytes+1), 1)} {
		p.observeChat(Codex, json.RawMessage(raw))
	}
	p.observeChat(Codex, json.RawMessage(fixture))
	if len(got) != 1 || got[0].Content.Text != "safe" || p.DroppedChatFrames() != 6 {
		t.Fatal("invalid frames crashed or were admitted", p.DroppedChatFrames())
	}
	for _, raw := range []string{
		`{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"thinking_delta","delta":"PRIVATE_SENTINEL"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"PRIVATE_SENTINEL"}]}}`,
	} {
		p.observeChat(Pi, json.RawMessage(raw))
	}
	if len(got) != 1 {
		t.Fatal("reasoning or final snapshot emitted as a delta")
	}
}

func TestCodexChatPreAcknowledgementKeepsExactTurn(t *testing.T) {
	var got []ChatUpdate
	p := &codexProcess{wireProcess: &wireProcess{threadID: "thread-1", observe: func(ev AdapterEvent) { got = append(got, *ev.Chat) }}}
	frame := `{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","delta":"safe"}}`
	p.observeCodexChat(json.RawMessage(frame))
	p.observeCodexChat(json.RawMessage(strings.Replace(frame, "turn-1", "foreign", 1)))
	if len(got) != 0 {
		t.Fatal("pre-ack output published")
	}
	p.flushCodexChat("turn-1")
	if len(got) != 1 || got[0].Content.Text != "safe" || p.DroppedChatFrames() != 1 || len(p.chatPending) != 0 {
		t.Fatal("pre-ack output lost ownership")
	}
	for i := 0; i < 5; i++ {
		p.observeCodexChat(json.RawMessage(frame))
	}
	if len(p.chatPending) != 4 || p.DroppedChatFrames() != 2 {
		t.Fatal("pre-ack buffer unbounded")
	}
}

func TestChatLiveOnlyBindingOverflowAndJournalIsolation(t *testing.T) {
	// No API or journal exists: any accidental fallthrough to durable telemetry
	// fails this test, including if a producer mistakenly also sets Kind.
	binding := ChatBinding{"tenant", "principal", "run", "generation", "session"}
	c := newSessionChat(binding, ChatCapabilities{"native", true, true})
	e := &owned{record: Record{TenantID: binding.TenantID, PrincipalID: binding.PrincipalID, RunID: binding.RunID, Generation: binding.Generation, State: "running"}, harness: HarnessSession{ID: binding.SessionID}, process: &fakeProcess{}, chat: c}
	s := &Supervisor{tenantID: binding.TenantID, principalID: binding.PrincipalID, generation: binding.Generation, runs: map[string]*owned{binding.RunID: e}}
	for _, other := range []ChatBinding{{"foreign", "principal", "run", "generation", "session"}, {"tenant", "foreign", "run", "generation", "session"}, {"tenant", "principal", "run", "old", "session"}, {"tenant", "principal", "run", "generation", "foreign"}} {
		if _, _, err := s.SubscribeChat(other); err == nil {
			t.Fatal("foreign stream admitted")
		}
	}
	before, _ := json.Marshal(e.record)
	chunk := chatText("PRIVATE_SENTINEL")
	s.observe(e, AdapterEvent{Chat: &chunk, Kind: "usage", OutputTokensDelta: 12})
	events, cancel, err := s.SubscribeChat(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if ev := <-events; ev.Capabilities == nil || ev.Capabilities.Steer != "native" {
		t.Fatal("capability snapshot missing")
	}
	select {
	case <-events:
		t.Fatal("text replayed to new subscriber")
	default:
	}
	state := chatState("running")
	s.observe(e, AdapterEvent{Chat: &state})
	if ev := <-events; ev.Update.State != "running" || ev.Binding != binding {
		t.Fatal("state or binding lost")
	}
	for i := 0; i < chatQueueLimit+2; i++ {
		s.observe(e, AdapterEvent{Chat: &chunk})
	}
	for i := 0; i < chatQueueLimit; i++ {
		<-events
	}
	s.observe(e, AdapterEvent{Chat: &chunk})
	if ev := <-events; ev.DroppedEvents != 2 || ev.Update.Content.Text != "PRIVATE_SENTINEL" {
		t.Fatal("overflow not reported")
	}
	after, _ := json.Marshal(e.record)
	if !bytes.Equal(before, after) {
		t.Fatal("live event changed durable record")
	}

	e.harnessArchived = true
	s.observe(e, AdapterEvent{Chat: &chunk})
	select {
	case <-events:
		t.Fatal("archived stream received output")
	default:
	}
	if _, _, err := s.SubscribeChat(binding); !errors.Is(err, ErrNotOwned) {
		t.Fatal("archived stream admitted")
	}
	c.close()
	if _, ok := <-events; ok {
		t.Fatal("closed session retained content")
	}
}

func TestChatSubscriberBoundAndCancellation(t *testing.T) {
	c := newSessionChat(ChatBinding{}, ChatCapabilities{})
	var cancels []func()
	for i := 0; i < chatSubscriberLimit; i++ {
		_, cancel, err := c.subscribe()
		if err != nil {
			t.Fatal(err)
		}
		cancels = append(cancels, cancel)
	}
	if _, _, err := c.subscribe(); !errors.Is(err, ErrUnsupported) {
		t.Fatal("subscriber bound ignored")
	}
	for _, cancel := range cancels {
		cancel()
		cancel()
	}
	events, cancel, err := c.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	c.publish(chatText("PRIVATE_SENTINEL"))
	cancel()
	if _, ok := <-events; ok {
		t.Fatal("cancellation retained queued chunks")
	}
}

func TestClaudeBridgeProjectsNativeChatWithoutPayloads(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for Claude bridge fixture")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bridgePath, sdkPath := filepath.Join(root, "bridge.mjs"), filepath.Join(root, "sdk.mjs")
	if err := os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	const sdk = `export function query({options}) {
 if(!options.includePartialMessages) throw Error('missing partial messages');
 return {streamInput:async()=>{},close:()=>{},async *[Symbol.asyncIterator](){
 yield {type:'system',subtype:'init',session_id:'session',model:'model',capabilities:[]};
 yield {type:'stream_event',event:{type:'content_block_delta',delta:{type:'text_delta',text:'Hello'}}};
 yield {type:'stream_event',event:{type:'content_block_delta',delta:{type:'thinking_delta',thinking:'PRIVATE_SENTINEL'}}};
 yield {type:'stream_event',parent_tool_use_id:'subagent',event:{type:'content_block_delta',delta:{type:'text_delta',text:'PRIVATE_SENTINEL'}}};
 const block={type:'tool_use',id:'call-1',name:'Bash',input:{command:'PRIVATE_SENTINEL'}};
 yield {type:'stream_event',event:{type:'content_block_start',content_block:block}};
 yield {type:'assistant',message:{content:[block,{type:'text',text:'PRIVATE_SENTINEL'}]}};
 yield {type:'user',message:{content:[{type:'tool_result',tool_use_id:'call-1',content:'PRIVATE_SENTINEL'}]}};
 yield {type:'assistant',message:{content:[{type:'tool_use',id:'approval',name:'mcp__aeon__aeon_request_approval',input:{}}]}};
 yield {type:'unknown',payload:'PRIVATE_SENTINEL'};
 yield {type:'result',subtype:'success',is_error:false,result:'fixture-final',modelUsage:{model:{inputTokens:1,outputTokens:1}},total_cost_usd:0};
 }};
}`
	if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root)
	cmd.Stdin = strings.NewReader(`{"op":"start","purpose":"managed","read_only_review":true,"prompt":"Fixture","capabilities":[]}` + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bridge fixture: %v", err)
	}
	if bytes.Contains(out, []byte("PRIVATE_SENTINEL")) {
		t.Fatal("bridge leaked payload")
	}
	var updates []ChatUpdate
	p := &wireProcess{observe: func(ev AdapterEvent) { updates = append(updates, *ev.Chat) }}
	lines := bufio.NewScanner(bytes.NewReader(out))
	for lines.Scan() {
		p.observeChat(Claude, lines.Bytes())
	}
	want := []ChatUpdate{chatState("running"), chatText("Hello"), chatTool("call-1", "Bash", "in_progress"), chatTool("call-1", "Bash", "completed"), chatTool("approval", "mcp__aeon__aeon_request_approval", "in_progress"), chatState("requires_action"), chatState("idle")}
	if !reflect.DeepEqual(updates, want) || p.DroppedChatFrames() != 1 {
		t.Fatalf("bridge normalization: %#v; dropped %d", updates, p.DroppedChatFrames())
	}
}

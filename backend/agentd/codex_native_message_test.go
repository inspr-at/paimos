package agentd

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type nativeFrameWriter chan []byte

func (w nativeFrameWriter) Write(p []byte) (int, error) {
	w <- append([]byte(nil), p...)
	return len(p), nil
}
func (w nativeFrameWriter) Close() error { return nil }

func TestCodexNativeMessageBindsThreadTurnAndClosedTool(t *testing.T) {
	frames := make(nativeFrameWriter, 8)
	sent := make(chan NativeMessage, 4)
	p := &codexProcess{stdin: frames, threadID: "owned-thread", turnID: "owned-turn", streamDone: make(chan struct{}), nativeMessages: newNativeMessageExecutor(func(_ context.Context, id string, m NativeMessage) NativeMessageReceipt {
		if id != "call-1" {
			t.Error("native call identity changed")
		}
		sent <- m
		return NativeMessageReceipt{MessageID: "receipt", Delivered: true}
	})}
	p.nativeReplies = newNativeReplyQueue(p.streamDone)
	defer close(p.streamDone)
	for _, tc := range []struct {
		thread, turn, tool string
		accepted           bool
	}{
		{"foreign", "owned-turn", NativeMessageTool, false}, {"owned-thread", "foreign", NativeMessageTool, false}, {"owned-thread", "owned-turn", "shell", false}, {"owned-thread", "owned-turn", NativeMessageTool, true},
	} {
		params, _ := json.Marshal(map[string]any{"threadId": tc.thread, "turnId": tc.turn, "tool": tc.tool, "callId": "call-1", "arguments": NativeMessage{To: "claude:peer", Body: "status"}})
		p.handleNativeMessage(codexRPCMessage{ID: json.RawMessage(`42`), Method: "item/tool/call", Params: params})
		select {
		case raw := <-frames:
			var frame struct {
				ID     int `json:"id"`
				Result struct {
					Success bool `json:"success"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &frame) != nil || frame.ID != 42 || frame.Result.Success != tc.accepted {
				t.Fatalf("invalid result %s", raw)
			}
		case <-time.After(time.Second):
			t.Fatal("missing native result")
		}
	}
	if len(sent) != 1 {
		t.Fatalf("calls=%d", len(sent))
	}
}

func TestNativeRejectionDoesNotBlockReaderBehindControlWrite(t *testing.T) {
	frames := make(nativeFrameWriter, 1)
	done := make(chan struct{})
	defer close(done)
	p := &codexProcess{stdin: frames, nativeReplies: newNativeReplyQueue(done)}
	p.writeMu.Lock()
	locked := true
	defer func() {
		if locked {
			p.writeMu.Unlock()
		}
	}()
	id := json.RawMessage(`42`)
	returned := make(chan struct{})
	go func() { p.handleNativeMessage(codexRPCMessage{ID: id, Params: json.RawMessage(`{}`)}); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("reader blocked behind control write")
	}
	copy(id, []byte(`99`))
	p.writeMu.Unlock()
	locked = false
	select {
	case raw := <-frames:
		var frame struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.ID != 42 {
			t.Fatal("async request id changed")
		}
	case <-time.After(time.Second):
		t.Fatal("rejection reply missing")
	}
}

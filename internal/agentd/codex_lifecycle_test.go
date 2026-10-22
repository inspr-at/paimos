// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

type codexSyntheticInput struct{ requests chan []byte }

func (w codexSyntheticInput) Write(b []byte) (int, error) {
	w.requests <- append([]byte(nil), b...)
	return len(b), nil
}
func (codexSyntheticInput) Close() error { return nil }

type codexLifecycleFixture struct {
	proc      *codexProcess
	output    *io.PipeWriter
	processed chan struct{}
	start     chan error
	mu        sync.Mutex
	reports   []sessionusage.UsageReport
}

func newCodexLifecycleFixture(t *testing.T) *codexLifecycleFixture {
	t.Helper()
	return newCodexLifecycleFixtureWithStream(t, iotest.OneByteReader)
}

func newCodexLifecycleFixtureWithStream(t *testing.T, stream func(io.Reader) io.Reader) *codexLifecycleFixture {
	t.Helper()
	reader, writer := io.Pipe()
	capture, err := sessionusage.NewManagedCodex("synthetic-thread", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	input := codexSyntheticInput{requests: make(chan []byte, 1)}
	f := &codexLifecycleFixture{output: writer, processed: make(chan struct{}, 32), start: make(chan error, 1)}
	wire := &wireProcess{stdin: input, stdout: reader, pending: map[string]chan json.RawMessage{}, threadID: "synthetic-thread", protocol: "jsonrpc", readDone: make(chan struct{}), waitDone: make(chan struct{})}
	wire.observe = func(e AdapterEvent) {
		if e.SessionUsage != nil {
			f.mu.Lock()
			f.reports = append(f.reports, *e.SessionUsage)
			f.mu.Unlock()
		}
	}
	f.proc = &codexProcess{wireProcess: wire, usage: capture, done: make(chan bool, 1)}
	wire.setOnEvent(func(raw json.RawMessage) { f.proc.notification(raw); f.processed <- struct{}{} })
	go wire.read(stream(reader))
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
		<-wire.readDone
		f.proc.eventMu.Lock()
		if f.proc.idleTimer != nil {
			f.proc.idleTimer.Stop()
		}
		f.proc.eventMu.Unlock()
	})
	go func() { f.start <- f.proc.startTurn(t.Context(), StartRequest{Profile: Profile{Model: "model-a"}}) }()
	select {
	case b := <-input.requests:
		var req struct {
			Method string `json:"method"`
			Params struct {
				Thread string `json:"threadId"`
			} `json:"params"`
		}
		if json.Unmarshal(b, &req) != nil || req.Method != "turn/start" || req.Params.Thread != "synthetic-thread" {
			t.Fatal("start lost requested thread")
		}
	case <-time.After(time.Second):
		t.Fatal("missing start request")
	}
	return f
}
func (f *codexLifecycleFixture) emit(t *testing.T, raw string) {
	t.Helper()
	if _, err := io.WriteString(f.output, raw+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.processed:
	case <-time.After(time.Second):
		t.Fatal("event not processed")
	}
}
func (f *codexLifecycleFixture) ack(t *testing.T, result string) error {
	t.Helper()
	if _, err := io.WriteString(f.output, `{"jsonrpc":"2.0","id":1,`+result+"}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.start:
		return err
	case <-time.After(time.Second):
		t.Fatal("start did not settle")
		return nil
	}
}
func (f *codexLifecycleFixture) assertProvisional(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reports {
		if !r.Provisional {
			t.Fatal("irreversible final receipt before confirmed boundary")
		}
	}
}
func (f *codexLifecycleFixture) settle(t *testing.T, tail ...string) error {
	t.Helper()
	return f.proc.waitForTurn(func(context.Context) error {
		f.assertProvisional(t)
		for _, raw := range tail {
			f.emit(t, raw)
		}
		close(f.proc.waitDone)
		return f.output.Close()
	}, time.Second)
}

const lifecycleUsage = `{"method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread","turnId":"synthetic-turn","model":"model-a","tokenUsage":{"total":{"inputTokens":100,"outputTokens":20,"cachedInputTokens":30}}}}`
const lifecycleTerminal = `{"method":"turn/completed","params":{"threadId":"synthetic-thread","turn":{"id":"synthetic-turn","status":"completed"}}}`
const lifecycleAck = `"result":{"turn":{"id":"synthetic-turn","status":"inProgress"}}`

// Risk (AEON-1082): turn/steer ACK or output from the already-running turn
// fabricates delivery. Use the real app-server adapter with a held turn and
// explicit input-item/completion notifications, including wrong-turn controls.
func TestChatSteerReceiptsUseExactCodexInputReactions(t *testing.T) {
	f := newCodexLifecycleFixture(t)
	if err := f.ack(t, lifecycleAck); err != nil {
		t.Fatal(err)
	}
	api := newFakeRelayAPI()
	api.binding.InputTiming = "native"
	input := chatInput("m1", "sent")
	input.Message.Level = "steer"
	api.inputs = []ChatInput{input}
	r := NewChatRelay(api, HarnessSession{ID: "session"}, ChatRelayOptions{Steer: "native", RequireInputCorrelation: true,
		DeliverTimed: func(ctx context.Context, id, body, _ string, writing func()) error {
			writing()
			return f.proc.ControlChat(ctx, "steer", chatInputText(id, body), id)
		},
	})
	seq := uint64(1)
	running := chatState("running")
	r.Observe(ChatSessionEvent{Sequence: seq, Update: &running})
	f.proc.eventMu.Lock()
	f.proc.observe = func(ev AdapterEvent) {
		if ev.Chat != nil {
			seq++
			r.Observe(ChatSessionEvent{Sequence: seq, InputID: ev.InputID, Update: ev.Chat})
		}
	}
	f.proc.eventMu.Unlock()
	result := make(chan error, 1)
	go func() { result <- r.deliverNext(t.Context(), api.binding) }()
	var request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			Thread string `json:"threadId"`
			Turn   string `json:"expectedTurnId"`
			Input  []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	select {
	case raw := <-f.proc.stdin.(codexSyntheticInput).requests:
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native steer never reached the held active turn")
	}
	if request.Method != "turn/steer" || request.Params.Thread != "synthetic-thread" || request.Params.Turn != "synthetic-turn" || len(request.Params.Input) != 1 || request.Params.Input[0].Text != chatInputText("m1", input.Message.Body) {
		t.Fatalf("wrong native operation/target: %+v", request)
	}
	ack, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]string{"turnId": "synthetic-turn"}})
	if _, err := f.output.Write(append(ack, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if len(r.receipts) != 0 {
		t.Fatal("native write ACK invented a receipt")
	}
	item := func(thread, turn, text string) string {
		raw, _ := json.Marshal(map[string]any{"method": "item/started", "params": map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"type": "userMessage", "id": "input-item", "content": []map[string]string{{"type": "text", "text": text}}}}})
		return string(raw)
	}
	for _, raw := range []string{
		item("other-thread", "synthetic-turn", request.Params.Input[0].Text),
		item("synthetic-thread", "earlier-turn", request.Params.Input[0].Text),
		item("synthetic-thread", "synthetic-turn", "unrelated earlier input"),
	} {
		f.emit(t, raw)
		if len(r.receipts) != 0 {
			t.Fatal("unrelated input supplied receipt evidence")
		}
	}
	f.emit(t, item("synthetic-thread", "synthetic-turn", request.Params.Input[0].Text))
	if len(r.receipts) != 1 || r.receipts[0].message != "m1" || r.receipts[0].state != "delivered" {
		t.Fatalf("exact input reaction: %+v", r.receipts)
	}
	f.emit(t, lifecycleTerminal)
	if len(r.receipts) != 2 || r.receipts[1].message != "m1" || r.receipts[1].state != "read" {
		t.Fatalf("exact turn completion: %+v", r.receipts)
	}
}

func TestCodexAcknowledgedTerminalAndDrain(t *testing.T) {
	for _, tc := range []struct {
		name, ack, terminal, tail string
		pre, clean                bool
	}{
		{name: "fast completion", ack: lifecycleAck, terminal: lifecycleTerminal, pre: true, clean: true},
		{name: "post ack", ack: lifecycleAck, terminal: lifecycleTerminal, clean: true},
		{name: "failed start", ack: `"error":{"code":-1,"message":"PRIVATE_SENTINEL"}`, terminal: lifecycleTerminal, pre: true},
		{name: "malformed ack", ack: `"result":{"turn":{"status":"inProgress"}}`, terminal: lifecycleTerminal, pre: true},
		{name: "wrong ack turn", ack: strings.ReplaceAll(lifecycleAck, "synthetic-turn", "wrong-turn"), terminal: lifecycleTerminal, pre: true},
		{name: "missing terminal id", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, `"id":"synthetic-turn",`, "")},
		{name: "wrong terminal turn", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, "synthetic-turn", "wrong-turn")},
		{name: "wrong terminal thread", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, "synthetic-thread", "wrong-thread")},
		{name: "failed", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, `"completed"`, `"failed"`)},
		{name: "interrupted", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, `"completed"`, `"interrupted"`)},
		{name: "malformed terminal", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, `"status":"completed"`, `"status":42`)},
		{name: "conflicting error", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, `"status":"completed"`, `"status":"completed","error":{"message":"PRIVATE_SENTINEL"}`)},
		{name: "null error", ack: lifecycleAck, terminal: strings.ReplaceAll(lifecycleTerminal, `"status":"completed"`, `"status":"completed","error":null`), clean: true},
		{name: "duplicate buffered", ack: lifecycleAck, terminal: lifecycleTerminal, tail: lifecycleTerminal},
		{name: "failed buffered", ack: lifecycleAck, terminal: lifecycleTerminal, tail: strings.ReplaceAll(lifecycleTerminal, "turn/completed", "turn/failed")},
		{name: "usage buffered", ack: lifecycleAck, terminal: lifecycleTerminal, tail: lifecycleUsage},
		{name: "reroute buffered", ack: lifecycleAck, terminal: lifecycleTerminal, tail: `{"method":"model/rerouted","params":{"threadId":"synthetic-thread"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexLifecycleFixture(t)
			f.emit(t, lifecycleUsage)
			if tc.pre {
				f.emit(t, tc.terminal)
				select {
				case <-f.proc.done:
					t.Fatal("pre-ack terminal signalled completion")
				default:
				}
			}
			f.assertProvisional(t)
			err := f.ack(t, tc.ack)
			if tc.name == "failed start" || tc.name == "malformed ack" {
				if err == nil {
					t.Fatal("failed start accepted")
				}
				f.assertProvisional(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.pre {
				f.emit(t, tc.terminal)
			}
			tail := []string{}
			if tc.tail != "" {
				tail = append(tail, tc.tail)
			}
			err = f.settle(t, tail...)
			if (err == nil) != tc.clean {
				t.Fatalf("completion=%v expected clean=%t", err, tc.clean)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.reports) < 2 {
				t.Fatal("usage settlement missing")
			}
			for i, r := range f.reports {
				if !r.Provisional && (!tc.clean || i != len(f.reports)-1) {
					t.Fatal("invalid finality")
				}
			}
			if f.reports[len(f.reports)-1].Provisional == tc.clean {
				t.Fatal("final usage does not match boundary")
			}
		})
	}
}

func TestCodexPreAckContradictionsAreBounded(t *testing.T) {
	for _, tail := range []string{lifecycleTerminal, lifecycleUsage, strings.ReplaceAll(lifecycleTerminal, `"completed"`, `"failed"`)} {
		t.Run(tail[:24], func(t *testing.T) {
			f := newCodexLifecycleFixture(t)
			f.emit(t, lifecycleUsage)
			f.emit(t, lifecycleTerminal)
			// Arbitrarily many duplicate terminals occupy only one normalized candidate.
			for i := 0; i < 40; i++ {
				f.emit(t, tail)
			}
			if err := f.ack(t, lifecycleAck); err != nil {
				t.Fatal(err)
			}
			if err := f.settle(t); err == nil {
				t.Fatal("buffered contradiction upgraded to success")
			}
			f.assertProvisional(t)
		})
	}
}
func TestCodexDrainRequiresEOFAndOwnedExit(t *testing.T) {
	for _, mode := range []string{"reader_error", "malformed_tail", "drain_timeout", "exit_timeout", "stop_error", "no_terminal", "expired_after_drain"} {
		t.Run(mode, func(t *testing.T) {
			f := newCodexLifecycleFixture(t)
			if err := f.ack(t, lifecycleAck); err != nil {
				t.Fatal(err)
			}
			f.emit(t, lifecycleUsage)
			if mode != "no_terminal" {
				f.emit(t, lifecycleTerminal)
			} else {
				_ = f.output.Close()
			}
			err := f.proc.waitForTurn(func(ctx context.Context) error {
				if mode != "exit_timeout" {
					close(f.proc.waitDone)
				}
				switch mode {
				case "reader_error":
					_ = f.output.CloseWithError(errors.New("synthetic stream loss"))
				case "malformed_tail":
					_, _ = io.WriteString(f.output, "not-json\n")
					_ = f.output.Close()
				case "drain_timeout":
				default:
					_ = f.output.Close()
				}
				if mode == "stop_error" {
					return errors.New("synthetic stop rejection")
				}
				if mode == "expired_after_drain" {
					<-f.proc.readDone
					<-ctx.Done()
				}
				return nil
			}, 20*time.Millisecond)
			if err == nil {
				t.Fatal("incomplete drain accepted")
			}
			f.assertProvisional(t)
			// Timeout seals provisional exactly once; a delayed frame cannot revise it.
			if mode == "drain_timeout" {
				select {
				case <-f.proc.readDone:
				default:
					t.Fatal("timed out reader was not joined")
				}
				if _, err := io.WriteString(f.output, lifecycleTerminal+"\n"); err == nil {
					t.Fatal("timed out reader still accepts frames")
				}
				f.assertProvisional(t)
				_ = f.output.Close()
				if err := f.proc.waitForTurn(func(context.Context) error { return nil }, time.Second); err == nil {
					t.Fatal("later EOF upgraded a failed completion boundary")
				}
			}
		})
	}
}

func TestCodexWaitAbandonsBlockedObserver(t *testing.T) {
	for _, kind := range []string{"usage", "tool"} {
		t.Run(kind, func(t *testing.T) {
			// Use Scanner's normal buffering so a second frame is already admitted
			// to its buffer when the first observer stalls under eventMu.
			f := newCodexLifecycleFixtureWithStream(t, func(r io.Reader) io.Reader { return r })
			if err := f.ack(t, lifecycleAck); err != nil {
				t.Fatal(err)
			}
			f.emit(t, lifecycleUsage)
			f.emit(t, lifecycleTerminal)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			f.proc.eventMu.Lock()
			observe := f.proc.observe
			calls := 0 // read only after readDone joins the observer
			f.proc.observe = func(e AdapterEvent) {
				if kind == "usage" && e.SessionUsage != nil || kind == "tool" && e.Kind == "tool" {
					calls++
					if calls == 1 {
						close(entered)
						<-release
					}
				}
				observe(e)
			}
			f.proc.eventMu.Unlock()
			tail := `{"method":"item/started"}`
			if kind == "usage" {
				tail = strings.NewReplacer(":100", ":200", ":20", ":40", ":30", ":60").Replace(lifecycleUsage)
			}
			// Pipe.Write returns only after Scanner has consumed both frames.
			if _, err := io.WriteString(f.output, tail+"\n"+tail+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("observer did not enter")
			}
			if f.proc.eventMu.TryLock() {
				f.proc.eventMu.Unlock()
				t.Fatal("observer does not hold eventMu")
			}
			result := make(chan error, 1)
			go func() {
				result <- f.proc.waitForTurn(func(context.Context) error {
					close(f.proc.waitDone)
					return f.output.Close()
				}, 20*time.Millisecond)
			}()
			// Keep the real two-second cleanup bound; only shorten the drain.
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("unjoined observer accepted as clean completion")
				}
			case <-time.After(readerCleanupTimeout + time.Second):
				t.Fatal("Wait exceeded cleanup boundary while observer remained held")
			}
			select {
			case <-f.proc.readDone:
				t.Fatal("reader claimed completion while callback was held")
			default:
			}
			f.assertProvisional(t)
			// Repeat the public Wait while eventMu is still held. It must not
			// start another drain, lock eventMu, or touch process ownership.
			go func() { result <- f.proc.Wait() }()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("abandoned Wait returned success")
				}
			case <-time.After(time.Second):
				t.Fatal("later Wait blocked behind the abandoned observer")
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case <-f.proc.readDone:
			case <-time.After(time.Second):
				t.Fatal("released reader did not finish")
			}
			if f.proc.readErr == nil {
				t.Fatal("local close became true EOF")
			}
			if calls != 1 {
				t.Fatal("abandoned reader dispatched another buffered callback")
			}
			if err := f.proc.Wait(); err == nil {
				t.Fatal("late reader completion resurrected success")
			}
			f.proc.eventMu.Lock()
			// Even an independently delivered notification/finalization attempt
			// cannot resurrect an abandoned capture after the reader releases it.
			f.proc.notification(json.RawMessage(lifecycleTerminal))
			f.proc.sealUsage(true)
			f.proc.eventMu.Unlock()
			f.assertProvisional(t)
			f.mu.Lock()
			defer f.mu.Unlock()
			last := f.reports[len(f.reports)-1]
			wantInput := int64(100)
			if kind == "usage" {
				wantInput = 200
			}
			if last.InputTokens == nil || *last.InputTokens != wantInput {
				t.Fatal("admitted callback lost bounded provisional accounting")
			}
		})
	}
}

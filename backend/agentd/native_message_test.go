package agentd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNativeMessageClosedInput(t *testing.T) {
	valid := `{"to":"claude:peer","body":"status","reply_to":"","is_action_request":false,"expects_reply":false}`
	if _, err := DecodeNativeMessage([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(valid, `"body":"status"`, `"body":"`+strings.Repeat("x", 4097)+`"`, 1), valid + `{}`, strings.Replace(valid, `"body":"status"`, `"body":"\u0000"`, 1), strings.Replace(valid, `"to":"claude:peer"`, `"to":"--config"`, 1)} {
		if _, err := DecodeNativeMessage([]byte(raw)); err == nil {
			t.Fatal("invalid tool input accepted")
		}
	}
	for _, field := range []string{"sender", "project_id", "session_id", "worker_lease", "thread_id", "metadata", "delivery_level", "idempotency_key"} {
		if _, err := DecodeNativeMessage([]byte(strings.TrimSuffix(valid, "}") + `,"` + field + `":"forged"}`)); err == nil {
			t.Fatalf("model supplied %s", field)
		}
	}
	raw, _ := json.Marshal(StartRequest{})
	if strings.Contains(string(raw), "sendNativeMessage") {
		t.Fatal("callback crossed public start wire")
	}
}

func TestNativeMessageExecutorBoundsAndCancels(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	e := newNativeMessageExecutor(func(ctx context.Context, _ string, _ NativeMessage) NativeMessageReceipt {
		close(started)
		<-ctx.Done()
		close(canceled)
		return NativeMessageReceipt{Error: "canceled"}
	})
	done := make(chan struct{})
	results := make(chan NativeMessageReceipt, 2)
	body := []byte(`{"to":"claude:peer","body":"status"}`)
	e.run(done, "first", body, func(r NativeMessageReceipt) { results <- r })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("send not started")
	}
	e.run(done, "second", body, func(r NativeMessageReceipt) { results <- r })
	if r := <-results; r.Error != "send_busy" {
		t.Fatalf("result=%+v", r)
	}
	close(done)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("send survived child shutdown")
	}
}

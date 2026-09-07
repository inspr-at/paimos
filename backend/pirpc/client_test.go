// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/pirpctest"
)

func piHelperCommand(mode string) *exec.Cmd {
	return pirpctest.Command(piRPCHelperTest, mode)(os.Args[0])
}

func TestClientCorrelatedPromptAcceptance(t *testing.T) {
	cmd := piHelperCommand("serve")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	client := NewClient(stdout, stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := client.Call(ctx, "req-prompt", Command{Type: "prompt", Message: "secret-not-logged"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Command != "prompt" {
		t.Fatalf("response=%+v", response)
	}

	deadline := time.After(500 * time.Millisecond)
	var settled bool
	for {
		select {
		case event := <-client.Events():
			if event.Settled {
				settled = true
			}
		case <-deadline:
			if !settled {
				t.Fatal("expected agent_settled terminal event after acceptance")
			}
			return
		}
	}
}

func TestClientDuplicateResponseIgnored(t *testing.T) {
	cmd := piHelperCommand("duplicate-response")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	client := NewClient(stdout, stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := client.Call(ctx, "dup-id", Command{Type: "prompt", Message: "work"})
	if err != nil || !response.Success {
		t.Fatalf("first response err=%v response=%+v", err, response)
	}
}

func TestClientMalformedFrameFailsPending(t *testing.T) {
	pr, pw := io.Pipe()
	client := NewClient(pr, pw)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		_, _ = pw.Write([]byte("{bad-json\n"))
	}()

	_, err := client.Call(ctx, "bad-frame", Command{Type: "prompt", Message: "x"})
	if err == nil {
		t.Fatal("expected malformed frame error")
	}
}

func TestSanitizeEventRedactsQueueWithoutText(t *testing.T) {
	raw := json.RawMessage(`{"type":"queue_update","steering":["secret steer"],"followUp":["secret follow"]}`)
	event := SanitizeEvent(raw)
	if event.SteeringQueued != 1 || event.FollowUpQueued != 1 {
		t.Fatalf("event=%+v", event)
	}
	if strings.Contains(event.Type, "secret") {
		t.Fatalf("event leaked message text: %+v", event)
	}
}

func TestClientLateResponseAfterDeadline(t *testing.T) {
	cmd := piHelperCommand("late-response")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	client := NewClient(stdout, stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = client.Call(ctx, "late-id", Command{Type: "prompt", Message: "work"})
	if err == nil {
		t.Fatal("expected deadline error")
	}
}

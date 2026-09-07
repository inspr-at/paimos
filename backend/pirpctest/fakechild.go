// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package pirpctest holds fake pi --mode rpc child scaffolding for tests.
// Production adapter and daemon code must not import this package.
package pirpctest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const (
	HelperEnv               = "PAIMOS_PI_RPC_HELPER"
	ArgvEnv                 = "PAIMOS_PI_CHILD_ARGV"
	StreamingEnv            = "PAIMOS_PI_STREAMING"
	TraceEnv                = "PAIMOS_PI_CHILD_TRACE"
	maxFrameBytes           = 8 << 20
	piCodingAgentDirEnvName = "PI_CODING_AGENT_DIR"
)

type command struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Command returns a test-only Command seam that re-executes the current test
// binary as a fake Pi RPC child. helperTest must be the TestXxx host in the
// calling package.
func Command(helperTest, mode string, extraEnv ...string) func(string, ...string) *exec.Cmd {
	return func(path string, args ...string) *exec.Cmd {
		cmd := exec.Command(path, "-test.run=^"+helperTest+"$")
		env := append(os.Environ(),
			HelperEnv+"="+mode,
			ArgvEnv+"="+strings.Join(args, "\x1e"),
		)
		env = append(env, extraEnv...)
		cmd.Env = env
		return cmd
	}
}

// Run executes the fake child when HelperEnv is set. Tests host this from a
// TestXxx that returns immediately when the env is unset.
func Run() bool {
	mode := strings.TrimSpace(os.Getenv(HelperEnv))
	if mode == "" {
		return false
	}
	switch mode {
	case "serve":
		runServe()
	case "unicode-line":
		emitRaw(map[string]string{"type": "message_update", "content": "before\u2028inside\u2029after"})
	case "corrupt":
		fmt.Fprintln(os.Stdout, "{not-json")
	case "oversize":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", maxFrameBytes+1))
	case "eof-after-accept":
		runEOFAfterAccept()
	case "late-response":
		runLateResponse()
	case "duplicate-response":
		runDuplicateResponse()
	case "queue-abort":
		runQueueAbort()
	case "native-queue":
		runNativeQueue(false)
	case "accept-not-complete":
		runAcceptNotComplete()
	case "argv-echo":
		runArgvEcho()
	case "clear-eof":
		runClearEOF()
	case "agent-dir-echo":
		runAgentDirEcho()
	case "clear-extra":
		runNativeQueue(true)
	case "abort-fail":
		runAbortFail()
	default:
		os.Exit(2)
	}
	return true
}

func runServe() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			emitResponse("", "parse", false, "Failed to parse command")
			continue
		}
		trace("cmd=" + cmd.Type)
		switch cmd.Type {
		case "get_state":
			emitGetState(cmd.ID, "sess-owned", strings.TrimSpace(os.Getenv(StreamingEnv)) == "1", 0)
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			emitRaw(map[string]string{"type": "agent_start"})
			emitRaw(map[string]string{"type": "agent_settled"})
		case "steer":
			emitResponse(cmd.ID, "steer", true, "")
			emitRaw(map[string]any{"type": "queue_update", "steering": []string{"queued-steer"}, "followUp": []string{}})
		case "follow_up":
			emitResponse(cmd.ID, "follow_up", true, "")
			emitRaw(map[string]any{"type": "queue_update", "steering": []string{}, "followUp": []string{"queued-follow"}})
		case "abort":
			emitResponse(cmd.ID, "abort", true, "")
		case "clear_queue":
			emitRaw(map[string]any{
				"type": "response", "id": cmd.ID, "command": "clear_queue", "success": true,
				"data": map[string]any{"steering": []string{"saved-steer"}, "followUp": []string{"saved-follow"}},
			})
		default:
			emitResponse(cmd.ID, cmd.Type, false, "unsupported command")
		}
	}
}

func runEOFAfterAccept() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		trace("cmd=" + cmd.Type)
		switch cmd.Type {
		case "get_state":
			emitGetState(cmd.ID, "sess-eof", false, 0)
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			return
		}
	}
}

func runLateResponse() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		if cmd.Type == "prompt" {
			continue
		}
	}
	emitResponse("late-id", "prompt", true, "")
}

func runDuplicateResponse() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		if cmd.Type == "prompt" && cmd.ID != "" {
			emitResponse(cmd.ID, "prompt", true, "")
			emitResponse(cmd.ID, "prompt", true, "")
			return
		}
	}
}

func runQueueAbort() {
	scanner := bufio.NewScanner(os.Stdin)
	steerID, abortID := "", ""
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		trace("cmd=" + cmd.Type)
		switch cmd.Type {
		case "get_state":
			emitGetState(cmd.ID, "sess-queue-abort", false, 0)
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			emitRaw(map[string]string{"type": "agent_start"})
		case "steer":
			steerID = cmd.ID
			emitResponse(cmd.ID, "steer", true, "")
			emitRaw(map[string]any{"type": "queue_update", "steering": []string{cmd.Message}, "followUp": []string{}})
		case "abort":
			abortID = cmd.ID
			emitResponse(cmd.ID, "abort", true, "")
			if steerID != "" {
				emitRaw(map[string]any{"type": "queue_update", "steering": []string{"still-queued"}, "followUp": []string{}})
			}
		case "clear_queue":
			emitRaw(map[string]any{
				"type": "response", "id": cmd.ID, "command": "clear_queue", "success": true,
				"data": map[string]any{"steering": []string{"preserved"}, "followUp": []string{}},
			})
		}
	}
	if abortID == "" {
		os.Exit(1)
	}
}

// runNativeQueue models installed rpc.md: abort continues queued messages
// when they remain in the session. clear_queue returns exact steering/followUp.
func runNativeQueue(extra bool) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes)
	var mu sync.Mutex
	steering, followUp := []string{}, []string{}
	streaming := true
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			emitResponse("", "parse", false, "Failed to parse command")
			continue
		}
		trace("cmd=" + cmd.Type)
		mu.Lock()
		switch cmd.Type {
		case "get_state":
			pending := len(steering) + len(followUp)
			emitGetState(cmd.ID, "sess-native-queue", streaming, pending)
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			streaming = true
			emitRaw(map[string]string{"type": "agent_start"})
		case "steer":
			steering = append(steering, cmd.Message)
			emitResponse(cmd.ID, "steer", true, "")
			emitRaw(map[string]any{"type": "queue_update", "steering": copyStrings(steering), "followUp": copyStrings(followUp)})
		case "follow_up":
			followUp = append(followUp, cmd.Message)
			emitResponse(cmd.ID, "follow_up", true, "")
			emitRaw(map[string]any{"type": "queue_update", "steering": copyStrings(steering), "followUp": copyStrings(followUp)})
		case "clear_queue":
			returnedFollow := copyStrings(followUp)
			if extra {
				returnedFollow = append(returnedFollow, "ghost-extra")
			}
			emitRaw(map[string]any{
				"type": "response", "id": cmd.ID, "command": "clear_queue", "success": true,
				"data": map[string]any{"steering": copyStrings(steering), "followUp": returnedFollow},
			})
			steering, followUp = nil, nil
			emitRaw(map[string]any{"type": "queue_update", "steering": []string{}, "followUp": []string{}})
		case "abort":
			emitResponse(cmd.ID, "abort", true, "")
			if len(steering)+len(followUp) > 0 {
				trace("continued=true")
				streaming = true
				emitRaw(map[string]string{"type": "agent_start"})
			} else {
				streaming = false
				emitRaw(map[string]string{"type": "agent_settled"})
			}
		default:
			emitResponse(cmd.ID, cmd.Type, false, "unsupported command")
		}
		mu.Unlock()
	}
}

func runAcceptNotComplete() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "get_state":
			emitGetState(cmd.ID, "sess-accept-not-complete", false, 0)
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			emitRaw(map[string]string{"type": "agent_start"})
			return
		}
	}
}

func runArgvEcho() {
	scanner := bufio.NewScanner(os.Stdin)
	frozen := strings.Join(os.Args[1:], "|")
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		if cmd.Type == "get_state" {
			emitGetState(cmd.ID, frozen, false, 0)
		}
	}
}

func runClearEOF() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		trace("cmd=" + cmd.Type)
		switch cmd.Type {
		case "get_state":
			emitGetState(cmd.ID, "sess-clear-eof", true, 1)
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			emitRaw(map[string]string{"type": "agent_start"})
		case "follow_up", "steer":
			emitResponse(cmd.ID, cmd.Type, true, "")
		case "clear_queue":
			return
		case "abort":
			trace("continued=true")
			emitResponse(cmd.ID, "abort", true, "")
			emitRaw(map[string]string{"type": "agent_start"})
		}
	}
}

func runAbortFail() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes)
	var steering, followUp []string
	streaming := true
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			emitResponse("", "parse", false, "Failed to parse command")
			continue
		}
		trace("cmd=" + cmd.Type)
		switch cmd.Type {
		case "get_state":
			emitGetState(cmd.ID, "sess-abort-fail", streaming, len(steering)+len(followUp))
		case "prompt":
			emitResponse(cmd.ID, "prompt", true, "")
			streaming = true
			emitRaw(map[string]string{"type": "agent_start"})
		case "steer":
			steering = append(steering, cmd.Message)
			emitResponse(cmd.ID, "steer", true, "")
		case "follow_up":
			followUp = append(followUp, cmd.Message)
			emitResponse(cmd.ID, "follow_up", true, "")
		case "clear_queue":
			emitRaw(map[string]any{
				"type": "response", "id": cmd.ID, "command": "clear_queue", "success": true,
				"data": map[string]any{"steering": copyStrings(steering), "followUp": copyStrings(followUp)},
			})
			steering, followUp = nil, nil
		case "abort":
			emitResponse(cmd.ID, "abort", false, "abort refused")
		default:
			emitResponse(cmd.ID, cmd.Type, false, "unsupported command")
		}
	}
}

func runAgentDirEcho() {
	scanner := bufio.NewScanner(os.Stdin)
	pinned := strings.TrimSpace(os.Getenv(piCodingAgentDirEnvName))
	for scanner.Scan() {
		var cmd command
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
			continue
		}
		if cmd.Type == "get_state" {
			emitGetState(cmd.ID, pinned, false, 0)
		}
	}
}

func emitGetState(id, sessionID string, streaming bool, pending int) {
	provider, model, thinking := launchIntent()
	emitRaw(map[string]any{
		"type": "response", "id": id, "command": "get_state", "success": true,
		"data": map[string]any{
			"isStreaming": streaming, "pendingMessageCount": pending, "sessionId": sessionID,
			"thinkingLevel": thinking,
			"model":         map[string]string{"provider": provider, "id": model},
		},
	})
}

func launchIntent() (provider, model, thinking string) {
	provider, model, thinking = "anthropic", "claude-sonnet-4-20250514", "high"
	raw := strings.TrimSpace(os.Getenv(ArgvEnv))
	if raw == "" {
		return provider, model, thinking
	}
	args := strings.Split(raw, "\x1e")
	for index := 0; index+1 < len(args); index++ {
		switch args[index] {
		case "--provider":
			provider = args[index+1]
		case "--model":
			model = args[index+1]
		case "--thinking":
			thinking = args[index+1]
		}
	}
	return provider, model, thinking
}

func emitResponse(id, command string, success bool, errMsg string) {
	emitRaw(map[string]any{
		"type":    "response",
		"id":      id,
		"command": command,
		"success": success,
		"error":   errMsg,
	})
}

func emitRaw(value any) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > maxFrameBytes {
		return
	}
	body = append(body, '\n')
	_, _ = os.Stdout.Write(body)
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func trace(line string) {
	path := strings.TrimSpace(os.Getenv(TraceEnv))
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, line)
	_ = f.Close()
}

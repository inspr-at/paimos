// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	listenExitNoMessages         = 3
	listenExitAdapterUnavailable = 4
	listenDefaultPollInterval    = 2 * time.Second
)

type listenExitCode struct {
	code int
	err  error
}

func (e *listenExitCode) Error() string {
	if e.err == nil {
		return "listen stopped"
	}
	return e.err.Error()
}

func (e *listenExitCode) Unwrap() error { return e.err }

type inboxPage struct {
	Address    string            `json:"address"`
	Cursor     int64             `json:"cursor"`
	NextCursor int64             `json:"next_cursor"`
	Messages   []messageEnvelope `json:"messages"`
}

func listenCmd() *cobra.Command {
	var (
		projectRef    string
		address       string
		follow        bool
		ack           bool
		deliver       string
		deliverTarget string
		deliverMode   string
		enableGrok    bool
		pollInterval  time.Duration
	)
	c := &cobra.Command{
		Use:   "listen",
		Short: "Read and optionally deliver durable messages for one agent inbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(projectRef) == "" {
				return &usageError{msg: "--project is required"}
			}
			harness, agent, err := splitListenAddress(address)
			if err != nil {
				return &usageError{msg: err.Error()}
			}
			address = harness + ":" + agent
			deliver = strings.ToLower(strings.TrimSpace(deliver))
			if deliver != "" && deliver != "codex" && deliver != "claude" && deliver != "grok" {
				return &usageError{msg: "--deliver must be codex, claude, or grok"}
			}
			deliverMode = strings.ToLower(strings.TrimSpace(deliverMode))
			if deliverMode == "" {
				deliverMode = "queue"
			}
			if deliverMode != "queue" && deliverMode != "steer" {
				return &usageError{msg: "--deliver-mode must be queue or steer"}
			}
			if pollInterval <= 0 {
				return &usageError{msg: "--poll-interval must be greater than zero"}
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRefToID(client, projectRef)
			if err != nil {
				return reportError(err)
			}
			return runListen(cmd.Context(), client, projectID, address, agent, follow, ack || deliver != "", deliver, strings.TrimSpace(deliverTarget), deliverMode, enableGrok, pollInterval)
		},
	}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&address, "as", "", "receiver address <harness>:<registered-agent> (required)")
	c.Flags().BoolVar(&follow, "follow", false, "keep polling until interrupted")
	c.Flags().BoolVar(&ack, "ack", false, "durably acknowledge messages after output")
	c.Flags().StringVar(&deliver, "deliver", "", "deliver each message to codex, claude, or grok, then acknowledge")
	c.Flags().StringVar(&deliverTarget, "deliver-target", "", "Codex thread id, Claude Unix socket, or Grok session UUID (defaults to vendor environment)")
	c.Flags().StringVar(&deliverMode, "deliver-mode", "queue", "delivery mode: queue (wait until idle, default) or steer (mid-turn interrupt)")
	c.Flags().BoolVar(&enableGrok, "enable-grok-build-delivery", false, "enable the experimental Grok Build delivery adapter")
	c.Flags().DurationVar(&pollInterval, "poll-interval", listenDefaultPollInterval, "follow polling interval")
	return c
}

func splitListenAddress(raw string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 || !addressPartCLI.MatchString(parts[0]) || !addressPartCLI.MatchString(parts[1]) {
		return "", "", fmt.Errorf("--as must be <harness>:<registered-agent>")
	}
	return strings.ToLower(parts[0]), parts[1], nil
}

var addressPartCLI = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var grokSessionUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func runListen(ctx context.Context, client *Client, projectID int64, address, agent string, follow, acknowledge bool, deliver, target, mode string, enableGrok bool, pollInterval time.Duration) error {
	after, seen := int64(0), false
	for {
		page, err := fetchInbox(ctx, client, projectID, address, agent, after)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return reportError(err)
		}
		if len(page.Messages) > 0 {
			seen = true
			for _, message := range page.Messages {
				if err := emitOrDeliverMessage(ctx, message, deliver, target, mode, enableGrok); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					var unavailable *adapterUnavailableError
					if errors.As(err, &unavailable) {
						return &listenExitCode{code: listenExitAdapterUnavailable, err: err}
					}
					return err
				}
				after = message.Cursor
				if acknowledge {
					if err := ackInbox(ctx, client, projectID, address, agent, after); err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return reportError(err)
					}
				}
			}
		}
		if !follow {
			if !seen {
				return &listenExitCode{code: listenExitNoMessages}
			}
			return nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func fetchInbox(ctx context.Context, client *Client, projectID int64, address, agent string, after int64) (*inboxPage, error) {
	q := url.Values{"to": []string{address}, "limit": []string{"10"}}
	if after > 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	raw, err := client.doForAgentContext(ctx, "GET", fmt.Sprintf("/api/projects/%d/messages/listen?%s", projectID, q.Encode()), nil, agent)
	if err != nil {
		return nil, err
	}
	var page inboxPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decode inbox: %w", err)
	}
	return &page, nil
}

func ackInbox(ctx context.Context, client *Client, projectID int64, address, agent string, cursor int64) error {
	_, err := client.doForAgentContext(ctx, "POST", fmt.Sprintf("/api/projects/%d/messages/ack", projectID), map[string]any{
		"to": address, "cursor": cursor,
	}, agent)
	return err
}

func emitOrDeliverMessage(ctx context.Context, message messageEnvelope, deliver, target, mode string, enableGrok bool) error {
	if deliver == "" {
		if flagJSON {
			raw, err := json.Marshal(message)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(stdout, string(raw))
			return err
		}
		body := messageText(message)
		_, err := fmt.Fprintf(stdout, "cursor=%d  %s  %s → %s  thread=%s\n%s\n", message.Cursor, message.MessageID, message.From, message.To, message.ThreadID, body)
		return err
	}
	body := messageText(message)
	switch deliver {
	case "codex":
		return deliverCodex(ctx, body, target, mode)
	case "claude":
		return deliverClaude(ctx, body, target)
	case "grok":
		return deliverGrok(ctx, body, target, enableGrok)
	default:
		return fmt.Errorf("unsupported delivery adapter %q", deliver)
	}
}

func messageText(message messageEnvelope) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Kind == "text" && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

type adapterUnavailableError struct{ message string }

func (e *adapterUnavailableError) Error() string { return e.message }

func deliverCodex(ctx context.Context, body, target, mode string) error {
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	}
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_SESSION_ID"))
	}
	if target == "" {
		return &adapterUnavailableError{message: "Codex delivery requires --deliver-target or CODEX_THREAD_ID"}
	}
	// mode defaults to "queue" and was validated by the caller.
	// "queue" uses the codex CLI and waits until the thread is idle.
	// "steer" sends a JSON-RPC turn/steer request to the app-server control socket for mid-turn interruption.
	if mode == "" {
		mode = "queue"
	}
	if mode == "steer" {
		return deliverCodexSteer(ctx, body, target)
	}
	return deliverCodexQueue(ctx, body, target)
}

func deliverCodexQueue(ctx context.Context, body, target string) error {
	path, err := exec.LookPath("codex")
	if err != nil {
		return &adapterUnavailableError{message: "Codex delivery requires the codex CLI in PATH"}
	}
	// #nosec G204 G702 -- codex is resolved from the operator-controlled PATH;
	// all remaining values are fixed argv entries and no shell is involved.
	cmd := exec.CommandContext(ctx, path, "queue", "--thread", target, "--message", body)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("deliver to Codex thread: %w", err)
	}
	return nil
}

func deliverCodexSteer(ctx context.Context, body, threadID string) error {
	// Use codex app-server proxy to communicate with the app-server control socket.
	// The proxy provides JSON-RPC over stdio (newline-delimited JSON).
	// Requires a running app-server daemon: `codex app-server daemon start`
	// Control socket: ~/.codex/app-server-control/app-server-control.sock
	path, err := exec.LookPath("codex")
	if err != nil {
		return &adapterUnavailableError{message: "Codex steer requires the codex CLI in PATH"}
	}

	// #nosec G204 G702 -- codex is resolved from the operator-controlled PATH;
	// all remaining values are fixed argv entries and no shell is involved.
	cmd := exec.CommandContext(ctx, path, "app-server", "proxy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return &adapterUnavailableError{message: fmt.Sprintf("start codex app-server proxy (daemon not running?): %v", err)}
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	// JSON-RPC communication over stdio (newline-delimited JSON).
	encoder := json.NewEncoder(stdin)
	decoder := json.NewDecoder(stdout)

	// Handshake: initialize request (required) followed by initialized notification.
	initReq := map[string]any{
		"id":     0,
		"method": "initialize",
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name":    "paimos",
				"version": "1.0.0",
			},
		},
	}
	if err := encoder.Encode(initReq); err != nil {
		return fmt.Errorf("send initialize: %w", err)
	}

	var initResp map[string]any
	if err := decoder.Decode(&initResp); err != nil {
		return fmt.Errorf("read initialize response: %w", err)
	}
	if errObj, ok := initResp["error"].(map[string]any); ok {
		return fmt.Errorf("initialize error: %v", errObj["message"])
	}

	// Send initialized notification (no id, no response expected).
	initNotif := map[string]any{
		"method": "initialized",
	}
	if err := encoder.Encode(initNotif); err != nil {
		return fmt.Errorf("send initialized: %w", err)
	}

	// Query thread/turns/list to find the active turn.
	// Method: thread/turns/list
	// Params: {threadId: string}
	// Response: {data: Turn[]} where Turn.status is "inProgress" | "completed" | "interrupted" | "failed"
	turnsListReq := map[string]any{
		"id":     1,
		"method": "thread/turns/list",
		"params": map[string]any{"threadId": threadID},
	}
	if err := encoder.Encode(turnsListReq); err != nil {
		return fmt.Errorf("send thread/turns/list: %w", err)
	}

	var turnsListResp map[string]any
	if err := decoder.Decode(&turnsListResp); err != nil {
		return fmt.Errorf("read thread/turns/list response: %w", err)
	}

	if errObj, ok := turnsListResp["error"].(map[string]any); ok {
		return fmt.Errorf("thread/turns/list error: %v", errObj["message"])
	}

	result, ok := turnsListResp["result"].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid thread/turns/list response")
	}

	turns, ok := result["data"].([]any)
	if !ok || len(turns) == 0 {
		// No turns; fall back to queue.
		stdin.Close()
		_ = cmd.Wait()
		return deliverCodexQueue(ctx, body, threadID)
	}

	// Find the most recent turn with status "inProgress".
	var activeTurnID string
	for i := len(turns) - 1; i >= 0; i-- {
		turn, ok := turns[i].(map[string]any)
		if !ok {
			continue
		}
		turnID, _ := turn["id"].(string)
		status, _ := turn["status"].(string)
		if status == "inProgress" && turnID != "" {
			activeTurnID = turnID
			break
		}
	}

	if activeTurnID == "" {
		// No turn in progress; fall back to queue.
		stdin.Close()
		_ = cmd.Wait()
		return deliverCodexQueue(ctx, body, threadID)
	}

	// Send turn/steer JSON-RPC request.
	// Method: turn/steer
	// Params: {threadId: string, expectedTurnId: string, input: UserInput[]}
	// UserInput TextUserInput: {type: "text", text: string}
	// Response: {turnId: string}
	// Error codes: activeTurnNotSteerable (e.g. /review or /compact in progress)
	steerReq := map[string]any{
		"id":     2,
		"method": "turn/steer",
		"params": map[string]any{
			"threadId":       threadID,
			"expectedTurnId": activeTurnID,
			"input": []map[string]any{
				{"type": "text", "text": body},
			},
		},
	}
	if err := encoder.Encode(steerReq); err != nil {
		return fmt.Errorf("send turn/steer: %w", err)
	}

	var steerResp map[string]any
	if err := decoder.Decode(&steerResp); err != nil {
		return fmt.Errorf("read turn/steer response: %w", err)
	}

	if errObj, ok := steerResp["error"].(map[string]any); ok {
		code, _ := errObj["code"].(string)
		message, _ := errObj["message"].(string)
		// activeTurnNotSteerable: /review or /compact in progress.
		if code == "activeTurnNotSteerable" {
			return fmt.Errorf("turn/steer rejected (%s): %s", code, message)
		}
		return fmt.Errorf("turn/steer error: %s", message)
	}

	return nil
}

func deliverClaude(ctx context.Context, body, target string) error {
	// Claude Code messaging socket delivery is unsupported for mid-turn interrupts.
	// Official docs (https://code.claude.com/docs/en/headless.md) specify only
	// the optional auth line {"type":"auth","token":"..."}, not the message frame.
	//
	// Supported paths for Claude delivery:
	// - Idle turn: `claude -p --resume <session_id>` (new turn)
	// - Cloud session: `claude -p --cloud <session_id>` (queued follow-up)
	// - Cross-session: in-session SendMessage tool (receiver reads between tool calls)
	// - Channels: MCP notifications/claude/channel (research preview, allowlisted plugins)
	//
	// PAIMOS does not implement these paths yet. Print the message for human relay.
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"))
	}
	return &adapterUnavailableError{message: "Claude mid-turn delivery unsupported; use --resume / --cloud for idle turns or SendMessage tool for cross-session"}
}

func deliverGrok(ctx context.Context, body, target string, enabled bool) error {
	if !enabled {
		return &adapterUnavailableError{message: "Grok Build delivery is experimental; pass --enable-grok-build-delivery"}
	}
	if target == "" {
		target = strings.TrimSpace(os.Getenv("GROK_SESSION_ID"))
	}
	if !grokSessionUUIDPattern.MatchString(target) {
		return &adapterUnavailableError{message: "Grok Build delivery requires a canonical lowercase session UUID via --deliver-target or GROK_SESSION_ID"}
	}
	path, err := exec.LookPath("grok")
	if err != nil {
		return &adapterUnavailableError{message: "Grok Build delivery requires the grok CLI in PATH"}
	}
	// #nosec G204 G702 -- grok is resolved from the operator-controlled PATH;
	// all remaining values are fixed argv entries and no shell is involved.
	cmd := exec.CommandContext(ctx, path,
		"--single", body,
		"--resume", target,
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--tools", "",
		"--no-plan",
		"--no-subagents",
		"--disable-web-search",
		"--max-turns", "1",
		"--verbatim",
	)
	// The vendor response can contain session context. PAIMOS treats a zero exit
	// as the handoff acknowledgement and never captures, stores, or prints it.
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("deliver to Grok Build session: %w", err)
	}
	return nil
}

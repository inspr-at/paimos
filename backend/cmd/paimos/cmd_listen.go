// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bufio"
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

type deliveryOutcome struct {
	EffectiveLevel string
	FallbackReason string
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
	c.Flags().StringVar(&deliverTarget, "deliver-target", "", "legacy target for pre-bus messages; bus messages use their receiver-owned target version")
	c.Flags().StringVar(&deliverMode, "deliver-mode", "queue", "legacy pre-bus delivery mode; bus messages use their durable message level")
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

func runListen(ctx context.Context, client *Client, projectID int64, address, agent string, follow, acknowledge bool, deliver, target, legacyMode string, enableGrok bool, pollInterval time.Duration) error {
	after, seen := int64(0), false
	for {
		page, err := fetchInbox(ctx, client, projectID, address, agent, after, deliver)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return reportError(err)
		}
		if len(page.Messages) > 0 {
			seen = true
			for _, message := range page.Messages {
				outcome, err := emitOrDeliverMessage(ctx, message, deliver, target, legacyMode, enableGrok)
				if err != nil {
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
					if message.DeliveryWork != nil && deliver != "" {
						if outcome == nil {
							return errors.New("delivery completed without an outcome")
						}
						err = completeInboxDelivery(ctx, client, projectID, address, agent, message, *outcome)
					} else {
						err = ackInbox(ctx, client, projectID, address, agent, after)
					}
					if err != nil {
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

func completeInboxDelivery(ctx context.Context, client *Client, projectID int64, address, agent string, message messageEnvelope, outcome deliveryOutcome) error {
	_, err := client.doForAgentContext(ctx, "POST", fmt.Sprintf("/api/projects/%d/messages/delivery-complete", projectID), map[string]any{
		"to": address, "cursor": message.Cursor, "delivery_id": message.DeliveryWork.DeliveryID,
		"effective_level": outcome.EffectiveLevel, "fallback_reason": outcome.FallbackReason,
	}, agent)
	return err
}

func fetchInbox(ctx context.Context, client *Client, projectID int64, address, agent string, after int64, deliveryAdapter string) (*inboxPage, error) {
	q := url.Values{"to": []string{address}, "limit": []string{"10"}}
	if after > 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	if deliveryAdapter == "codex" {
		q.Set("delivery", "codex")
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

func emitOrDeliverMessage(ctx context.Context, message messageEnvelope, deliver, target, legacyMode string, enableGrok bool) (*deliveryOutcome, error) {
	if deliver == "" {
		if flagJSON {
			raw, err := json.Marshal(message)
			if err != nil {
				return nil, err
			}
			_, err = fmt.Fprintln(stdout, string(raw))
			return nil, err
		}
		body := messageText(message)
		_, err := fmt.Fprintf(stdout, "cursor=%d  %s  %s → %s  thread=%s\n%s\n", message.Cursor, message.MessageID, message.From, message.To, message.ThreadID, body)
		return nil, err
	}
	body := messageText(message)
	switch deliver {
	case "codex":
		return deliverCodexMessage(ctx, message, body, target, legacyMode)
	case "claude":
		return &deliveryOutcome{EffectiveLevel: "simple"}, deliverClaude(ctx, body, target)
	case "grok":
		return &deliveryOutcome{EffectiveLevel: "simple"}, deliverGrok(ctx, body, target, enableGrok)
	default:
		return nil, fmt.Errorf("unsupported delivery adapter %q", deliver)
	}
}

func deliverCodexMessage(ctx context.Context, message messageEnvelope, body, legacyTarget, legacyMode string) (*deliveryOutcome, error) {
	target := legacyTarget
	requested, maximum := message.DeliveryLevel, "steer"
	if requested == "" {
		if legacyMode == "steer" {
			requested = "steer"
		} else {
			requested = "simple"
		}
	}
	if message.DeliveryWork != nil {
		if message.DeliveryWork.Adapter != "codex" {
			return nil, &adapterUnavailableError{message: "message target is not a Codex adapter"}
		}
		target = message.DeliveryWork.TargetRef
		requested = message.DeliveryWork.RequestedLevel
		maximum = message.DeliveryWork.MaximumLevel
		if target == "" {
			return nil, &adapterUnavailableError{message: "message has no usable receiver-owned Codex thread target"}
		}
	}
	if requested != "steer" {
		return &deliveryOutcome{EffectiveLevel: "simple"}, deliverCodex(ctx, body, target)
	}
	if maximum == "simple" {
		if err := deliverCodex(ctx, body, target); err != nil {
			return nil, err
		}
		return &deliveryOutcome{EffectiveLevel: "simple", FallbackReason: "policy_capped"}, nil
	}
	steered, reason, err := deliverCodexSteer(ctx, body, target)
	if err != nil {
		return nil, err
	}
	if steered {
		return &deliveryOutcome{EffectiveLevel: "steer"}, nil
	}
	if err := deliverCodex(ctx, body, target); err != nil {
		return nil, err
	}
	return &deliveryOutcome{EffectiveLevel: "simple", FallbackReason: reason}, nil
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

func deliverCodex(ctx context.Context, body, target string) error {
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	}
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_SESSION_ID"))
	}
	if target == "" {
		return &adapterUnavailableError{message: "Codex delivery requires --deliver-target or CODEX_THREAD_ID"}
	}
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

type codexRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *codexRPCError) Error() string {
	return fmt.Sprintf("Codex app-server error %d: %s", e.Code, e.Message)
}

// deliverCodexSteer uses only the documented app-server proxy JSON-RPC
// sequence. A clean idle or rejected/raced steer is returned as a typed simple
// fallback decision; transport and handshake failures remain retryable errors.
func deliverCodexSteer(ctx context.Context, body, target string) (bool, string, error) {
	if strings.TrimSpace(target) == "" {
		return false, "", &adapterUnavailableError{message: "Codex steer requires a receiver-owned thread target"}
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return false, "", &adapterUnavailableError{message: "Codex delivery requires the codex CLI in PATH"}
	}
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// The daemon start primitive is idempotent and is the only supported way
	// to make the control service available; PAIMOS never opens its socket.
	daemon := exec.CommandContext(operationCtx, path, "app-server", "daemon", "start") // #nosec G204 G702 -- fixed operator-controlled executable and argv.
	daemon.Stdout = io.Discard
	daemon.Stderr = stderr
	if err := daemon.Run(); err != nil {
		return false, "", fmt.Errorf("start Codex app-server daemon: %w", err)
	}
	proxy := exec.CommandContext(operationCtx, path, "app-server", "proxy") // #nosec G204 G702 -- fixed operator-controlled executable and argv.
	stdin, err := proxy.StdinPipe()
	if err != nil {
		return false, "", fmt.Errorf("open Codex proxy stdin: %w", err)
	}
	stdoutPipe, err := proxy.StdoutPipe()
	if err != nil {
		return false, "", fmt.Errorf("open Codex proxy stdout: %w", err)
	}
	proxy.Stderr = stderr
	if err := proxy.Start(); err != nil {
		return false, "", fmt.Errorf("start Codex app-server proxy: %w", err)
	}
	encoder := json.NewEncoder(stdin)
	decoder := json.NewDecoder(bufio.NewReader(stdoutPipe))
	finish := func() error {
		_ = stdin.Close()
		if err := proxy.Wait(); err != nil && operationCtx.Err() == nil {
			return fmt.Errorf("Codex app-server proxy: %w", err)
		}
		return nil
	}
	var initialize json.RawMessage
	if err := codexRPCCall(encoder, decoder, 1, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "paimos", "version": Version},
	}, &initialize); err != nil {
		_ = finish()
		return false, "", fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		_ = finish()
		return false, "", fmt.Errorf("notify Codex app-server initialized: %w", err)
	}
	var turns struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := codexRPCCall(encoder, decoder, 2, "thread/turns/list", map[string]any{
		"threadId": target, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded",
	}, &turns); err != nil {
		_ = finish()
		return false, "", fmt.Errorf("list Codex thread turns: %w", err)
	}
	if len(turns.Data) == 0 || turns.Data[0].Status != "inProgress" {
		if err := finish(); err != nil {
			return false, "", err
		}
		return false, "idle", nil
	}
	var steerResult struct {
		TurnID string `json:"turnId"`
	}
	err = codexRPCCall(encoder, decoder, 3, "turn/steer", map[string]any{
		"threadId":       target,
		"expectedTurnId": turns.Data[0].ID,
		"input":          []map[string]string{{"type": "text", "text": body}},
	}, &steerResult)
	if err != nil {
		var remote *codexRPCError
		if errors.As(err, &remote) {
			_ = finish()
			return false, "not_steerable", nil
		}
		_ = finish()
		return false, "", fmt.Errorf("steer Codex turn: %w", err)
	}
	if err := finish(); err != nil {
		return false, "", err
	}
	return true, "", nil
}

func codexRPCCall(encoder *json.Encoder, decoder *json.Decoder, id int, method string, params any, result any) error {
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *codexRPCError  `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			return err
		}
		if len(response.ID) == 0 || string(response.ID) != strconv.Itoa(id) {
			continue
		}
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
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

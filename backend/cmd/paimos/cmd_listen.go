// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
			return runListen(cmd.Context(), client, projectID, address, agent, follow, ack || deliver != "", deliver, strings.TrimSpace(deliverTarget), enableGrok, pollInterval)
		},
	}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&address, "as", "", "receiver address <harness>:<registered-agent> (required)")
	c.Flags().BoolVar(&follow, "follow", false, "keep polling until interrupted")
	c.Flags().BoolVar(&ack, "ack", false, "durably acknowledge messages after output")
	c.Flags().StringVar(&deliver, "deliver", "", "deliver each message to codex, claude, or grok, then acknowledge")
	c.Flags().StringVar(&deliverTarget, "deliver-target", "", "Codex thread id, Claude Unix socket, or Grok session UUID (defaults to vendor environment)")
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

func runListen(ctx context.Context, client *Client, projectID int64, address, agent string, follow, acknowledge bool, deliver, target string, enableGrok bool, pollInterval time.Duration) error {
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
				if err := emitOrDeliverMessage(ctx, message, deliver, target, enableGrok); err != nil {
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

func emitOrDeliverMessage(ctx context.Context, message messageEnvelope, deliver, target string, enableGrok bool) error {
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
		return deliverCodex(ctx, body, target)
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

func deliverClaude(ctx context.Context, body, target string) error {
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"))
	}
	if target == "" {
		return &adapterUnavailableError{message: "Claude delivery requires --deliver-target or CLAUDE_CODE_MESSAGING_SOCKET"}
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", target)
	if err != nil {
		return &adapterUnavailableError{message: "Claude messaging socket is unavailable: " + err.Error()}
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	if token := os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN"); token != "" {
		if err := enc.Encode(map[string]string{"type": "auth", "token": token}); err != nil {
			return fmt.Errorf("authenticate Claude messaging socket: %w", err)
		}
	}
	if err := enc.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": body}}); err != nil {
		return fmt.Errorf("deliver to Claude messaging socket: %w", err)
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	// A successful write is the adapter acknowledgement. No credential or
	// vendor response is persisted by PAIMOS.
	return nil
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

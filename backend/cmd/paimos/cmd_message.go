// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
	"github.com/spf13/cobra"
)

type messageEnvelope struct {
	Cursor           int64                `json:"cursor"`
	MessageID        string               `json:"message_id"`
	ContextID        string               `json:"context_id"`
	From             string               `json:"from"`
	To               string               `json:"to"`
	TaskID           string               `json:"task_id,omitempty"`
	Role             string               `json:"role"`
	Metadata         map[string]any       `json:"metadata"`
	ReplyTo          string               `json:"reply_to,omitempty"`
	ThreadID         string               `json:"thread_id"`
	Hop              int                  `json:"hop"`
	Delivered        bool                 `json:"delivered"`
	HeldReason       string               `json:"held_reason,omitempty"`
	IsActionRequest  bool                 `json:"is_action_request"`
	CreatedAt        string               `json:"created_at"`
	ReadAt           string               `json:"read_at,omitempty"`
	DeliveryLevel    string               `json:"delivery_level"`
	DeliveryFallback string               `json:"delivery_fallback"`
	DeliveryTarget   any                  `json:"delivery_target"`
	DeliveryWork     *messageDeliveryWork `json:"delivery_work,omitempty"`
	Parts            []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"parts"`
}

type messageDeliveryWork struct {
	DeliveryID     string `json:"delivery_id"`
	Instance       string `json:"instance"`
	ProjectID      int64  `json:"project_id"`
	State          string `json:"state"`
	Adapter        string `json:"adapter,omitempty"`
	TargetKind     string `json:"target_kind,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	MaximumLevel   string `json:"maximum_level,omitempty"`
	RequestedLevel string `json:"requested_level"`
	FallbackReason string `json:"fallback_reason,omitempty"`
}

// tellCmd writes one durable, session-attributed ledger row.
func tellCmd() *cobra.Command {
	var projectRef, ticketRef, replyTo, threadID, message, messageFile, level string
	var actionRequest bool
	c := &cobra.Command{
		Use: "tell <harness>:<agent>", Short: "Send a durable message to a registered project agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(projectRef) == "" {
				return &usageError{msg: "--project is required"}
			}
			body, set, err := readMultilineInput(message, messageFile, "message")
			if err != nil {
				return err
			}
			if !set || strings.TrimSpace(body) == "" {
				return &usageError{msg: "--message or --message-file is required"}
			}
			level = strings.ToLower(strings.TrimSpace(level))
			if level != "simple" && level != "steer" {
				return &usageError{msg: "--level must be simple or steer"}
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRefToID(client, projectRef)
			if err != nil {
				return reportError(err)
			}
			var issueID *int64
			if strings.TrimSpace(ticketRef) != "" {
				id, err := resolveIssueRefToID(client, ticketRef)
				if err != nil {
					return reportError(err)
				}
				issueID = &id
			}
			payload := map[string]any{"to": strings.TrimSpace(args[0]), "body": body, "delivery_level": level}
			if actionRequest {
				payload["is_action_request"] = true
			}
			if issueID != nil {
				payload["issue_id"] = *issueID
			}
			if replyTo != "" {
				payload["reply_to"] = strings.TrimSpace(replyTo)
			}
			if threadID != "" {
				payload["thread_id"] = strings.TrimSpace(threadID)
			}
			raw, err := client.do("POST", fmt.Sprintf("/api/projects/%d/messages", projectID), payload)
			if err != nil {
				return reportError(err)
			}
			if flagJSON {
				fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
				return nil
			}
			var out messageEnvelope
			if err := json.Unmarshal(raw, &out); err != nil {
				return err
			}
			state := "delivered"
			if !out.Delivered {
				state = "held: " + out.HeldReason
			}
			fmt.Fprintf(stdout, "✓ %s → %s (%s)\nmessage: %s\nthread: %s · hop %d\n", out.From, out.To, state, out.MessageID, out.ThreadID, out.Hop)
			return nil
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id (required)")
	c.Flags().StringVar(&ticketRef, "ticket", "", "optional issue key or id:<n>")
	c.Flags().StringVar(&replyTo, "reply-to", "", "message id being answered")
	c.Flags().StringVar(&threadID, "thread", "", "conversation thread id")
	c.Flags().StringVarP(&message, "message", "m", "", "message body")
	c.Flags().StringVar(&messageFile, "message-file", "", "read message body from file, or - for stdin")
	c.Flags().StringVar(&level, "level", "simple", "delivery level: simple or steer")
	c.Flags().BoolVar(&actionRequest, "action-request", false, "mark as a human-gated action request; never deliver to an agent inbox")
	return c
}

func messageCmd() *cobra.Command {
	c := &cobra.Command{Use: "message", Short: "Read the durable agent message ledger"}
	c.AddCommand(messageListCmd(), messageGetCmd(), messageAllowCmd(), messageTargetCmd(), messageDeliveryCmd())
	return c
}

func messageTargetCmd() *cobra.Command {
	c := &cobra.Command{Use: "target", Short: "Manage receiver-owned delivery target versions"}
	c.AddCommand(messageTargetSetCmd(), messageTargetListCmd(), messageTargetRequeueCmd())
	return c
}

func messageTargetSetCmd() *cobra.Command {
	var projectRef, address, adapter, kind, ref, refFile, keyFile, maximumLevel, role string
	c := &cobra.Command{Use: "set", Short: "Register and enable a new encrypted target version", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(projectRef) == "" || strings.TrimSpace(address) == "" {
			return &usageError{msg: "--project and --address are required"}
		}
		adapterName := strings.ToLower(strings.TrimSpace(adapter))
		webhookAdapter := adapterName == harnessplugin.AdapterGrokBotRoutine
		if plugin, err := harnessplugin.Resolve(adapterName); err == nil && plugin.Kind() == harnessplugin.KindHTTPSWebhook {
			webhookAdapter = true
		}
		if strings.TrimSpace(ref) != "" && (strings.EqualFold(strings.TrimSpace(kind), harnessplugin.KindHTTPSWebhook) || webhookAdapter) {
			return &usageError{msg: "webhook capability URLs must use --target-ref-file (use - for stdin) so they do not enter process arguments"}
		}
		if refFile == "-" && keyFile == "-" {
			return &usageError{msg: "stdin can carry only one of --target-ref-file and --target-key-file"}
		}
		var targetRef string
		var set bool
		var err error
		if capabilityFile := strings.TrimSpace(refFile); capabilityFile != "" && (webhookAdapter || strings.EqualFold(strings.TrimSpace(kind), harnessplugin.KindHTTPSWebhook)) {
			// A webhook capability URL is itself a bearer secret: read it only
			// from a protected owner-only file (or stdin), never from an
			// ordinary readable path.
			raw, readErr := readProtectedSecretInput(capabilityFile, "--target-ref-file")
			if readErr != nil {
				return readErr
			}
			targetRef, set = string(raw), true
		} else {
			targetRef, set, err = readMultilineInput(ref, refFile, "target ref")
			if err != nil {
				return err
			}
		}
		if !set || strings.TrimSpace(targetRef) == "" {
			return &usageError{msg: "--target-ref or --target-ref-file is required"}
		}
		secret, hasSecret, err := readTargetKeyFile(keyFile)
		if err != nil {
			return err
		}
		// Fail closed before any network call: the sender key is required
		// exactly where the adapter sends one and refused everywhere else.
		if _, _, required, err := harnessplugin.SecretHeader(adapterName); err == nil {
			if required && !hasSecret {
				return &usageError{msg: adapterName + " targets require --target-key-file: the receiver-owned routine sender key as one line from a file, or - for stdin (never an argument)"}
			}
			if !required && hasSecret {
				return &usageError{msg: adapterName + " targets send no sender key; drop --target-key-file"}
			}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		projectID, err := resolveProjectRefToID(client, projectRef)
		if err != nil {
			return reportError(err)
		}
		payload := map[string]string{"address": address, "adapter": adapter, "target_kind": kind, "target_ref": strings.TrimSpace(targetRef), "maximum_level": maximumLevel, "role": role}
		if hasSecret {
			payload["target_secret"] = secret
		}
		raw, err := client.do("POST", fmt.Sprintf("/api/projects/%d/message-targets", projectID), payload)
		if err != nil {
			return reportError(err)
		}
		if flagJSON {
			fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
			return nil
		}
		var target struct {
			ID        string `json:"id"`
			Version   int    `json:"version"`
			HasSecret bool   `json:"has_secret"`
		}
		if err := json.Unmarshal(raw, &target); err != nil {
			return err
		}
		suffix := ""
		if target.HasSecret {
			suffix = " (sender key stored encrypted; never printed)"
		}
		fmt.Fprintf(stdout, "✓ enabled target %s version %d for %s%s\n", target.ID, target.Version, address, suffix)
		return nil
	}}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&address, "address", "", "receiver address (required)")
	c.Flags().StringVar(&adapter, "adapter", "", "registered harness plugin name (required)")
	c.Flags().StringVar(&kind, "kind", "", "plugin target kind (required)")
	c.Flags().StringVar(&ref, "target-ref", "", "receiver-owned target reference (webhook capabilities must use --target-ref-file)")
	c.Flags().StringVar(&refFile, "target-ref-file", "", "read target reference from file, or - for stdin")
	c.Flags().StringVar(&keyFile, "target-key-file", "", "read the receiver-owned sender key from file, or - for stdin; required by grok_bot_routine (sent as Authorization: Bearer on every wake) and never accepted as an argument")
	c.Flags().StringVar(&maximumLevel, "maximum-level", "simple", "receiver policy: simple or steer")
	c.Flags().StringVar(&role, "role", "primary", "primary or simple_fallback")
	return c
}

// maxProtectedSecretInputBytes bounds a one-line capability value before any
// validation runs.
const maxProtectedSecretInputBytes = 4096

// readProtectedSecretInput reads one capability value for a file-only flag.
// "-" reads stdin. Any other path must be a protected file: it is opened
// without following symlinks and verified as a regular, single-linked file
// owned by the effective user with owner-only permissions (0600/0400) — the
// same policy as external-stage handoff credentials. Group- or world-readable
// files, symlinks, hard links, directories, devices, and files owned by
// another user are refused before a single byte is read. Errors carry the
// flag, the path, and the reason, never the contents.
func readProtectedSecretInput(path, flag string) ([]byte, error) {
	var reader io.Reader
	if path == "-" {
		reader = os.Stdin
	} else {
		file, err := openExternalStageSecretFile(path)
		if err != nil {
			return nil, &usageError{msg: fmt.Sprintf("%s %s refused: it must be a regular file owned by you with owner-only permissions (chmod 0600), not a symlink or hard link (%v)", flag, path, err)}
		}
		defer file.Close()
		reader = file
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxProtectedSecretInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", flag, err)
	}
	if len(raw) > maxProtectedSecretInputBytes {
		return nil, &usageError{msg: flag + " must be one short line; the input is larger than 4096 bytes"}
	}
	return raw, nil
}

// readTargetKeyFile reads one raw sender key from a protected file or stdin.
// There is deliberately no --target-key flag: a credential must never enter
// process arguments, shell history, or ps output, and it is never echoed back.
func readTargetKeyFile(path string) (string, bool, error) {
	if path == "" {
		return "", false, nil
	}
	raw, err := readProtectedSecretInput(path, "--target-key-file")
	if err != nil {
		return "", false, err
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", false, &usageError{msg: "--target-key-file is empty"}
	}
	if strings.ContainsAny(secret, "\r\n") {
		return "", false, &usageError{msg: "--target-key-file must contain exactly one line: the raw sender key"}
	}
	return secret, true, nil
}

func messageTargetListCmd() *cobra.Command {
	var projectRef, address string
	c := &cobra.Command{Use: "list", Short: "List non-secret target versions", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(projectRef) == "" {
			return &usageError{msg: "--project is required"}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		projectID, err := resolveProjectRefToID(client, projectRef)
		if err != nil {
			return reportError(err)
		}
		query := url.Values{}
		if strings.TrimSpace(address) != "" {
			query.Set("address", strings.TrimSpace(address))
		}
		raw, err := client.do("GET", fmt.Sprintf("/api/projects/%d/message-targets?%s", projectID, query.Encode()), nil)
		if err != nil {
			return reportError(err)
		}
		fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
		return nil
	}}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&address, "address", "", "optional receiver address")
	return c
}

func messageTargetRequeueCmd() *cobra.Command {
	var projectRef, address string
	c := &cobra.Command{Use: "requeue", Short: "Attach current targets to never-attempted target_missing deliveries", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(projectRef) == "" || strings.TrimSpace(address) == "" {
			return &usageError{msg: "--project and --address are required"}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		projectID, err := resolveProjectRefToID(client, projectRef)
		if err != nil {
			return reportError(err)
		}
		raw, err := client.do("POST", fmt.Sprintf("/api/projects/%d/message-targets/requeue", projectID), map[string]string{"address": address})
		if err != nil {
			return reportError(err)
		}
		fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
		return nil
	}}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&address, "address", "", "receiver address (required)")
	return c
}

func messageDeliveryCmd() *cobra.Command {
	var projectRef string
	c := &cobra.Command{Use: "deliveries", Short: "List redacted message delivery state", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(projectRef) == "" {
			return &usageError{msg: "--project is required"}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		projectID, err := resolveProjectRefToID(client, projectRef)
		if err != nil {
			return reportError(err)
		}
		raw, err := client.do("GET", fmt.Sprintf("/api/projects/%d/message-deliveries", projectID), nil)
		if err != nil {
			return reportError(err)
		}
		fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
		return nil
	}}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	return c
}

func messageAllowCmd() *cobra.Command {
	var projectRef, receiver string
	c := &cobra.Command{
		Use:   "allow <sender-address>",
		Short: "Allow a registered sender to deliver to one receiver inbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(projectRef) == "" {
				return &usageError{msg: "--project is required"}
			}
			if strings.TrimSpace(receiver) == "" {
				return &usageError{msg: "--for is required"}
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			pid, err := resolveProjectRefToID(client, projectRef)
			if err != nil {
				return reportError(err)
			}
			raw, err := client.do("POST", fmt.Sprintf("/api/projects/%d/message-allowlist", pid), map[string]string{
				"receiver": strings.TrimSpace(receiver), "sender": strings.TrimSpace(args[0]),
			})
			if err != nil {
				return reportError(err)
			}
			if flagJSON {
				fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
			} else {
				fmt.Fprintf(stdout, "✓ %s may deliver to %s\n", strings.TrimSpace(args[0]), strings.TrimSpace(receiver))
			}
			return nil
		},
	}
	c.Flags().StringVarP(&projectRef, "project", "p", "", "project key or numeric id (required)")
	c.Flags().StringVar(&receiver, "for", "", "receiver address (required)")
	return c
}

func messageListCmd() *cobra.Command {
	var projectRef, to, thread string
	var after int64
	var limit int
	c := &cobra.Command{Use: "list", Short: "List messages by addressee, thread, and cursor", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(projectRef) == "" {
			return &usageError{msg: "--project is required"}
		}
		if strings.TrimSpace(to) == "" {
			return &usageError{msg: "--to is required so listen reads remain addressee-scoped and security-framed"}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		pid, err := resolveProjectRefToID(client, projectRef)
		if err != nil {
			return reportError(err)
		}
		q := url.Values{}
		if to != "" {
			q.Set("to", strings.TrimSpace(to))
		}
		if thread != "" {
			q.Set("thread", strings.TrimSpace(thread))
		}
		if after > 0 {
			q.Set("after", strconv.FormatInt(after, 10))
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		raw, err := client.do("GET", fmt.Sprintf("/api/projects/%d/messages?%s", pid, q.Encode()), nil)
		if err != nil {
			return reportError(err)
		}
		if flagJSON {
			fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
			return nil
		}
		var out struct {
			Messages []messageEnvelope `json:"messages"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		for _, m := range out.Messages {
			text := ""
			if len(m.Parts) > 0 {
				text = m.Parts[0].Text
			}
			fmt.Fprintf(stdout, "cursor=%d  %s  %s → %s  hop=%d  %s\n  %s\n", m.Cursor, m.MessageID, m.From, m.To, m.Hop, m.CreatedAt, text)
		}
		return nil
	}}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id (required)")
	c.Flags().StringVar(&to, "to", "", "addressee inbox (required)")
	c.Flags().StringVar(&thread, "thread", "", "filter by thread id")
	c.Flags().Int64Var(&after, "after", 0, "resume after numeric ledger cursor")
	c.Flags().IntVar(&limit, "limit", 10, "maximum delivered messages (1-10)")
	return c
}

func messageGetCmd() *cobra.Command {
	var projectRef string
	c := &cobra.Command{Use: "get <message-id>", Short: "Get one durable message", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(projectRef) == "" {
			return &usageError{msg: "--project is required"}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		pid, err := resolveProjectRefToID(client, projectRef)
		if err != nil {
			return reportError(err)
		}
		raw, err := client.do("GET", fmt.Sprintf("/api/projects/%d/messages/%s", pid, url.PathEscape(args[0])), nil)
		if err != nil {
			return reportError(err)
		}
		fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
		return nil
	}}
	c.Flags().StringVar(&projectRef, "project", "", "project key or numeric id (required)")
	return c
}

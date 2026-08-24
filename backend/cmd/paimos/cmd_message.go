// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type messageEnvelope struct {
	Cursor     int64  `json:"cursor"`
	MessageID  string `json:"message_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	TaskID     string `json:"task_id"`
	ThreadID   string `json:"thread_id"`
	Hop        int    `json:"hop"`
	Delivered  bool   `json:"delivered"`
	HeldReason string `json:"held_reason"`
	CreatedAt  string `json:"created_at"`
	Parts      []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"parts"`
}

// tellCmd writes one durable, session-attributed ledger row.
func tellCmd() *cobra.Command {
	var projectRef, ticketRef, replyTo, threadID, message, messageFile string
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
			payload := map[string]any{"to": strings.TrimSpace(args[0]), "body": body}
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
	return c
}

func messageCmd() *cobra.Command {
	c := &cobra.Command{Use: "message", Short: "Read the durable agent message ledger"}
	c.AddCommand(messageListCmd(), messageGetCmd())
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

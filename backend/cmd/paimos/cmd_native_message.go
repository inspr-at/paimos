// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/spf13/cobra"
	"io"
	"net/http"
)

// The owner sends one bounded frame on stdin; no lease or body enters argv.
func nativeMessageCmd() *cobra.Command {
	var projectID int64
	var session, agent, key string
	c := &cobra.Command{Use: "send-native", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		fail := errors.New("native message send failed")
		if projectID <= 0 || uuid.Validate(session) != nil || uuid.Validate(key) != nil || agent == "" {
			return fail
		}
		raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 40<<10))
		if err != nil || len(raw) >= 40<<10 {
			return fail
		}
		var input struct {
			Lease   string          `json:"worker_lease"`
			Message json.RawMessage `json:"message"`
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !managedharness.ValidWorkerLease(input.Lease) {
			return fail
		}
		message, err := agentd.DecodeNativeMessage(input.Message)
		if err != nil {
			return fail
		}
		body, _ := json.Marshal(message)
		client, err := instanceClient()
		if err != nil {
			return fail
		}
		// #nosec G704 -- fixed route on the configured operator instance.
		req, err := http.NewRequestWithContext(cmd.Context(), "POST", joinInstanceAPI(client.baseURL, fmt.Sprintf("/api/projects/%d/harness-sessions/%s/messages", projectID, session)), bytes.NewReader(body))
		if err != nil {
			return fail
		}
		client.prepareRequest(req, true, "application/json", "application/json")
		req.Header.Set(agentAttrHeader, agent)
		req.Header.Set(sessionAttrHeader, session)
		req.Header.Set(harnessWorkerLeaseHeader, input.Lease)
		req.Header.Set(idempotencyHeader, key)
		response, err := client.doRequest(req)
		if err != nil {
			return fail
		}
		var receipt agentd.NativeMessageReceipt
		if json.Unmarshal(response, &receipt) != nil || uuid.Validate(receipt.MessageID) != nil || uuid.Validate(receipt.ThreadID) != nil {
			return fail
		}
		return json.NewEncoder(stdout).Encode(receipt)
	}}
	c.Flags().Int64Var(&projectID, "project-id", 0, "owned project")
	c.Flags().StringVar(&session, "session", "", "owned public session")
	c.Flags().StringVar(&agent, "agent", "", "owned agent")
	c.Flags().StringVar(&key, "idempotency-key", "", "native call key")
	return c
}

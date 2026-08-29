//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package agentdwire is the dependency-neutral local client used by Agent
// Intercom's leased worker plugin. It contains no vendor auth or payload log.
package agentdwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ControlRequest struct {
	CorrelationID string `json:"correlation_id"`
	Text          string `json:"text,omitempty"`
}

type Receipt struct {
	Operation       string    `json:"operation"`
	SessionID       string    `json:"session_id"`
	Identity        string    `json:"identity"`
	RequestedLevel  string    `json:"requested_level"`
	EffectiveLevel  string    `json:"effective_level"`
	FallbackReason  string    `json:"fallback_reason"`
	Primitive       string    `json:"primitive"`
	CorrelationID   string    `json:"correlation_id"`
	VendorMessageID string    `json:"vendor_message_id,omitempty"`
	AppliedAt       time.Time `json:"applied_at"`
}

func Steer(ctx context.Context, socket, sessionID string, request ControlRequest) (Receipt, error) {
	parsed, parseErr := uuid.Parse(sessionID)
	if !filepath.IsAbs(socket) || parseErr != nil || parsed.String() != sessionID || strings.ContainsAny(socket, "\x00\r\n") {
		return Receipt{}, errors.New("agentd target is invalid")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Receipt{}, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agentd/v1/sessions/"+sessionID+"/steer", strings.NewReader(string(body)))
	if err != nil {
		return Receipt{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.Do(httpRequest)
	if err != nil {
		return Receipt{}, errors.New("agentd local transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Receipt{}, fmt.Errorf("agentd managed steer rejected with HTTP %d", response.StatusCode)
	}
	var receipt Receipt
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, errors.New("agentd returned an invalid receipt")
	}
	return receipt, nil
}

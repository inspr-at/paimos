//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentdwire

import (
	"context"
	"errors"
	"time"
)

var (
	ErrSessionUnavailable    = errors.New("agentd managed session is unavailable")
	ErrCapabilityUnavailable = errors.New("agentd managed steer capability is unavailable")
	ErrTransportUnavailable  = errors.New("agentd local transport is unavailable")
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

func Steer(context.Context, string, string, ControlRequest) (Receipt, error) {
	return Receipt{}, errors.New("agentd Unix transport is unsupported")
}

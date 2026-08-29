// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	AdapterManagedHarness = "managed_harness"
	KindHarnessSession    = "harness_session"
)

// ManagedPlugin declares the durable delivery cap for a paimos-agentd-owned
// harness. The daemon leases and completes work through the message bus; this
// plugin intentionally never starts a vendor process or performs delivery.
type ManagedPlugin struct{}

func (ManagedPlugin) Name() string         { return AdapterManagedHarness }
func (ManagedPlugin) Kind() string         { return KindHarnessSession }
func (ManagedPlugin) MaximumLevel() string { return LevelSteer }
func (ManagedPlugin) Mode() string         { return ModeLocal }

func (ManagedPlugin) ValidateTarget(_ context.Context, ref string) error {
	if !utf8.ValidString(ref) || len([]byte(ref)) < 1 || len([]byte(ref)) > 256 || strings.ContainsAny(ref, "\x00\r\n") || strings.Contains(ref, "://") || strings.HasPrefix(ref, "/") {
		return &Error{Code: CodeTargetRefInvalid, Message: "managed harness session references must be 1 to 256 opaque UTF-8 bytes, not a path or URL"}
	}
	return nil
}

func (ManagedPlugin) Deliver(context.Context, DeliverRequest) (DeliverResult, error) {
	return DeliverResult{}, &UnavailableError{Message: "managed harness delivery is owned by the attributed listen worker"}
}

func init() { _ = Register(ManagedPlugin{}) }

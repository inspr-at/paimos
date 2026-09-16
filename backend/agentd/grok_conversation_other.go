//go:build !darwin

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
)

func (a *GrokConversationAdapter) startNativeConversation(_ context.Context, _ StartRequest, _ GrokConversationOptions) (GrokConversationExecution, error) {
	return nil, errors.New("native Grok conversation confinement is unavailable on this platform")
}

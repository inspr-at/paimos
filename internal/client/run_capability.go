// SPDX-License-Identifier: AGPL-3.0-only
package client

import (
	"context"
	"net/http"

	"github.com/inspr-at/paimos/internal/workerlease"
)

type runContentCapabilityKey struct{}

// WithRunContentCapability binds one daemon operation to its in-memory server
// proof, including when a caller injects an HTTP transport. An explicit empty
// value clears content authority for metadata settlement and safety retries.
func WithRunContentCapability(ctx context.Context, proof string) context.Context {
	return context.WithValue(ctx, runContentCapabilityKey{}, proof)
}

func applyRunContentCapability(ctx context.Context, h http.Header) {
	if proof, ok := ctx.Value(runContentCapabilityKey{}).(string); ok {
		h.Del(workerlease.RunCapabilityHeader)
		if proof != "" {
			h.Set(workerlease.RunCapabilityHeader, proof)
		}
	}
}

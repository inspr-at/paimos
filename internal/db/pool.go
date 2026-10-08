// SPDX-License-Identifier: AGPL-3.0-only

package db

import "context"

type backgroundKey struct{}

// Background marks server-owned work, including goroutines spawned by a loop.
// Request contexts must never inherit this marker.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

func IsBackground(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundKey{}).(bool)
	return v
}

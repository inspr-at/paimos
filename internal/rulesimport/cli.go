// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"encoding/json"
	"io"

	"github.com/inspr-at/paimos/internal/client"
)

// Options default to a local preview. Apply is intent, never authorization.
// The configured API client is obtained lazily, only after local checks pass.
type Options struct {
	Request Request
	Apply   bool
	Target  Target
}

// Run prints the complete local proposal before any API call, followed by a
// draft receipt only on success. Failure keeps the preview available on stdout.
// Template trust refusals print nothing, so private source text cannot escape
// through a public-template error/report. Re-run with an explicit local context.
func Run(ctx context.Context, options Options, stdout io.Writer, connect func() (*client.Client, error)) error {
	p, err := Build(ctx, options.Request)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return err
	}
	if !options.Apply {
		return nil
	}
	if _, err := MapDraft(p); err != nil {
		return err
	}
	if err := validateTarget(options.Target); err != nil {
		return err
	}
	if connect == nil {
		return draftRefusal("configured API client required")
	}
	c, err := connect()
	if err != nil {
		return draftRefusal("configured API client unavailable")
	}
	result, err := ApplyDraft(ctx, c, p, options.Target)
	if err != nil {
		return err
	}
	return enc.Encode(result)
}

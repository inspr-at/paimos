// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PublishAccountMetadata accepts only an explicit display/grant projection.
// The server verifies profile ownership and the registering principal again.
// Repeated identical reports do not create repeated server audit events.
func (r *Remote) PublishAccountMetadata(ctx context.Context, accountID string, metadata AccountMetadata) error {
	if metadata.AllowedProfileIDs == nil || len(metadata.AllowedProfileIDs) > 256 || strings.TrimSpace(metadata.Label) == "" {
		return errors.New("invalid account metadata")
	}
	for _, value := range []string{metadata.Label, metadata.Plan, metadata.HostLabel} {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 || strings.ContainsFunc(value, unicode.IsControl) {
			return errors.New("invalid account metadata")
		}
	}
	return r.Client.Do(ctx, "PUT", "/api/agent-accounts/"+url.PathEscape(accountID)+"/metadata", metadata, nil)
}

// Local account routing deliberately continues to use the existing adapter's
// opaque account key -> private home/expected identity mapping. CodexBar 0.66.0
// `usage --help` documents --provider codex --all-accounts, --account and
// --account-index as usage-query selectors, not Codex launch selectors.
// A coordinator can map a CodexBar usage label to an enrolled key locally,
// then request that account's UUID through RunCreate.requested_account_id.
// Never infer a CODEX_HOME from a CodexBar label, copy authentication files,
// publish raw CodexBar output, or convert a percentage to invented token units.
// Only independently configured allowance windows feed the existing ledger.

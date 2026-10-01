// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
)

var sourceIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validSourceID(s string) bool { return sourceIDRE.MatchString(s) }
func digest(b []byte) string      { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func captureBinding(opt Options) string {
	// Fixed schema and values, no vendor text. Bind billing metadata too, so a
	// continued capture cannot silently change the body of an already sent receipt.
	b, _ := json.Marshal([]string{"aeon-capture-v2", opt.Source, opt.SessionID, opt.SourceSessionID, opt.Model, opt.BillingMode, opt.AccountID, opt.AccountLabel, opt.SubscriptionLabel})
	return digest(b)
}
func checkPrefix(raw []byte, opt Options, binding string) error {
	p := opt.Previous
	if p == nil {
		return nil
	}
	if p.Binding != binding || !digestRE.MatchString(p.Digest) || p.Bytes < 0 || p.Bytes > len(raw) || digest(raw[:p.Bytes]) != p.Digest {
		return fmt.Errorf("%w: checkpoint binding or capture prefix mismatch", ErrRejected)
	}
	if p.Bytes > 0 && p.Bytes < len(raw) && raw[p.Bytes-1] != '\n' && raw[p.Bytes] != '\n' {
		return fmt.Errorf("%w: continuation is not at a record boundary", ErrRejected)
	}
	if p.Final && (!opt.Final || p.Bytes != len(raw)) {
		return fmt.Errorf("%w: capture was finalized", ErrRejected)
	}
	return nil
}

func unwrapRecord(fields map[string]json.RawMessage) (map[string]json.RawMessage, string, error) {
	raw, wrapped := fields["event"]
	if !wrapped {
		return fields, "", nil
	}
	if len(fields) != 2 {
		return nil, "", fmt.Errorf("%w: capture envelope fields", ErrMalformed)
	}
	id, err := parseString(fields["record_id"])
	if err != nil {
		return nil, "", err
	}
	id, err = canonicalUUID(id)
	if err != nil || id == "" {
		return nil, "", fmt.Errorf("%w: capture record UUID required", ErrRejected)
	}
	event, err := decodeObject(raw, nil)
	return event, id, err
}

// Validate identities only at documented locations. Never walk prompt/tool
// payloads, use them as metadata, or copy arbitrary keys/values into errors.
func checkSourceIdentity(fields map[string]json.RawMessage, opt Options) error {
	groups := []map[string]json.RawMessage{fields}
	if raw, ok := fields["params"]; ok {
		params, err := decodeObject(raw, nil)
		if err != nil {
			return err
		}
		groups = append(groups, params)
	}
	kind, _ := parseString(fields["type"])
	if opt.Source == "opencode" && kind == "step_finish" {
		part, err := objectField(fields, "part")
		if err != nil {
			return err
		}
		groups = append(groups, part)
	}
	if kind == "session_meta" {
		payload, err := objectField(fields, "payload")
		if err != nil {
			return err
		}
		if raw, ok := payload["id"]; ok {
			id, err := parseString(raw)
			if err != nil || id != opt.SourceSessionID {
				return fmt.Errorf("%w: source session mismatch", ErrRejected)
			}
		}
	}
	if kind == "turn_context" {
		payload, err := objectField(fields, "payload")
		if err != nil {
			return err
		}
		model, err := modelFields(payload)
		if err != nil {
			return err
		}
		// Context is evidence against a fixed-model assertion, never a fallback
		// attribution for later model-less usage.
		if opt.Model != "" && model != "" && model != opt.Model {
			return fmt.Errorf("%w: model changed within bound capture", ErrAmbiguous)
		}
	}
	for _, g := range groups {
		for _, k := range []string{"session_id", "sessionId", "sessionID", "thread_id", "threadId"} {
			if raw, ok := g[k]; ok {
				id, err := parseString(raw)
				if err != nil || id != opt.SourceSessionID {
					return fmt.Errorf("%w: source session mismatch", ErrRejected)
				}
			}
		}
	}
	return nil
}

func checkReports(old, next []UsageReport) error {
	for _, before := range old {
		var after *UsageReport
		for i := range next {
			if next[i].Model == before.Model {
				after = &next[i]
				break
			}
		}
		if after == nil {
			return fmt.Errorf("%w: model disappeared", ErrRejected)
		}
		for _, pair := range [][2]*int64{{before.InputTokens, after.InputTokens}, {before.OutputTokens, after.OutputTokens}, {before.CachedInputTokens, after.CachedInputTokens}, {before.ReasoningTokens, after.ReasoningTokens}} {
			if pair[0] != nil && (pair[1] == nil || *pair[1] < *pair[0]) {
				return fmt.Errorf("%w: known cumulative counter decreased or became unknown", ErrRejected)
			}
		}
		if before.InputTokens != nil && before.CachedInputTokens != nil && after.InputTokens != nil && after.CachedInputTokens != nil && *after.InputTokens-*after.CachedInputTokens < *before.InputTokens-*before.CachedInputTokens {
			return fmt.Errorf("%w: uncached cumulative counter decreased", ErrRejected)
		}
	}
	return nil
}

// ParseCheckpoint accepts only the parser's small local continuity document.
func ParseCheckpoint(raw []byte) (*Checkpoint, error) {
	if len(raw) > 1024 {
		return nil, fmt.Errorf("%w: checkpoint too large", ErrMalformed)
	}
	fields, err := decodeObject(raw, allow("binding", "bytes", "digest", "final"))
	if err != nil || len(fields) != 4 {
		return nil, fmt.Errorf("%w: checkpoint schema", ErrMalformed)
	}
	n, err := parseCount(fields["bytes"])
	if err != nil || n > maxInput {
		return nil, fmt.Errorf("%w: checkpoint byte count", ErrMalformed)
	}
	var c Checkpoint
	if err := json.Unmarshal(raw, &c); err != nil || c.Bytes < 0 || c.Bytes > maxInput || !digestRE.MatchString(c.Binding) || !digestRE.MatchString(c.Digest) || !bytes.Equal(fields["final"], []byte("true")) && !bytes.Equal(fields["final"], []byte("false")) {
		return nil, fmt.Errorf("%w: checkpoint schema", ErrMalformed)
	}
	return &c, nil
}

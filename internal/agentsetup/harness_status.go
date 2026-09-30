// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
)

// HarnessIssue retains the diagnostic locally while exposing only its code.
type HarnessIssue struct {
	Reason string
	Err    error
}

func (e *HarnessIssue) Error() string { return e.Err.Error() }
func (e *HarnessIssue) Unwrap() error { return e.Err }

// HarnessFailureReason never classifies private diagnostics by matching text.
func HarnessFailureReason(err error) string {
	if err == nil {
		return ""
	}
	var issue *HarnessIssue
	if errors.As(err, &issue) {
		return issue.Reason
	}
	return "dependency_invalid"
}

// Reason codes are one vocabulary for blocked_accounts (per account, local
// status only) and harness_details (per harness, local status and the server):
//
//	repin_pending        a recorded Claude repin waits for active Claude runs; no fix
//	dependency_invalid   a pinned runtime dependency failed at probe or launch
//	pin_missing          no interpreter pin is recorded, but the launcher needs one
//	pin_partial          a pin lacks its path or version
//	pin_drifted          the pinned interpreter now reports another version
//	pin_invalid          the pinned path is unusable or no longer matches
//	pin_unsafe           the pinned path is writable by others or inside the workspace
//	login_required       the vendor sign-in is missing or not the approved identity
//	starting             not probed yet; no fix
//	harness_failed       the approved launcher failed to start
//	cli_unavailable      Claude's approved CLI executable changed or is missing
//	profile_permissions  the pi profile directory is not private
//
// The pin_* codes come from the static check before launch; dependency_invalid
// is the runtime failure of the same dependencies. For Claude the pins are the
// shared Node/SDK pins. Newer daemons may send other bounded code tokens: they
// stay visible as needing attention with the raw code and no guessed fix.
//
// A ready harness may also list attention_accounts: a proper subset of its
// enrolled accounts that still need a fix while a sibling can work. Reasons
// use this same vocabulary. Commands are derived again from the reason and
// are never stored on the attention entry. attention_count is the full size
// of that subset, or a lower bound when an older report stopped at
// legacyAttentionCap names without a count. attention_truncated is true when
// the stored list is shorter than the count or that older report did not
// prove the list was complete. An omitted account is not ready.

const (
	PinMissing    = "pin_missing"
	PinPartial    = "pin_partial"
	PinDrifted    = "pin_drifted"
	PinInvalid    = "pin_invalid"
	PinUnsafe     = "pin_unsafe"
	FixAddHarness = "add_harness"
	FixRepin      = "repin"
	FixLogin      = "login"
	FixRestart    = "restart"
)

// HarnessReasons lists the known reason codes in the order documented above.
var HarnessReasons = []string{"repin_pending", "dependency_invalid", PinMissing, PinPartial, PinDrifted, PinInvalid, PinUnsafe, "login_required", "starting", "harness_failed", "cli_unavailable", "profile_permissions", "binding_missing", "probe_pending", "probe_timeout", "probe_failed", "capacity_capture", "capacity_timeout"}

// HarnessFix is the one fix form for blocked_accounts and harness_details: a
// kind and the exact CLI line. Commands are derived from the harness and reason
// here and by the server, never accepted from daemon telemetry.
type HarnessFix struct {
	Kind    string `json:"kind"`
	Command string `json:"command"`
}

// AttentionAccountLimit bounds the stored attention_accounts list. OpenAPI
// maxItems matches it. The full total still travels in attention_count.
const AttentionAccountLimit = 32

// legacyAttentionCap is the attention_accounts limit from before attention_count
// and attention_truncated were reported. A list of that length with no count
// has not proved that every blocked account is present.
const legacyAttentionCap = 5

// AccountAttention names one enrolled account that still needs a fix while
// its harness stays ready because another account of that harness can work.
type AccountAttention struct {
	AccountID string `json:"account_id"`
	Reason    string `json:"reason"`
}

// AttentionBlock is the validated partial block for one ready harness.
// Count is the full number of blocked enrollments, or a lower bound when
// Truncated is set and the sender did not prove a larger total. Truncated
// means an account missing from Accounts is not ready.
type AttentionBlock struct {
	Accounts  []AccountAttention
	Count     int
	Truncated bool
}

// HarnessDetail uses the same reason tokens and structured fix as BlockedAccount.
// Unknown bounded tokens survive version skew; local diagnostics never leave the host.
// Attention is set only for a ready harness and only for a proper subset.
type HarnessDetail struct {
	State              string             `json:"state"`
	Reason             string             `json:"reason,omitempty"`
	Fix                HarnessFix         `json:"fix,omitzero"`
	Attention          []AccountAttention `json:"attention_accounts,omitempty"`
	AttentionCount     int                `json:"attention_count,omitempty"`
	AttentionTruncated bool               `json:"attention_truncated,omitempty"`
}

// Ignore advisory extensions and legacy string fixes without weakening the
// strict decoder for lifecycle identities, fences or cleanup acknowledgements.
// A malformed attention list is ignored; it does not drop the rest of the detail.
func (d *HarnessDetail) UnmarshalJSON(raw []byte) error {
	var wire struct {
		State     string          `json:"state"`
		Reason    string          `json:"reason"`
		Fix       json.RawMessage `json:"fix"`
		Attention json.RawMessage `json:"attention_accounts"`
		Count     json.RawMessage `json:"attention_count"`
		Truncated json.RawMessage `json:"attention_truncated"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*d = HarnessDetail{State: wire.State, Reason: wire.Reason}
	if len(wire.Fix) > 0 && wire.Fix[0] == '{' {
		_ = json.Unmarshal(wire.Fix, &d.Fix)
	}
	d.Attention = decodeAttention(wire.Attention)
	if n, ok := decodeAttentionCount(wire.Count); ok {
		d.AttentionCount = n
	}
	d.AttentionTruncated = decodeAttentionTruncated(wire.Truncated)
	return nil
}

func decodeAttentionCount(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 1 || n > 10000 {
		return 0, false
	}
	return n, true
}

func decodeAttentionTruncated(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var v bool
	return json.Unmarshal(raw, &v) == nil && v
}

func decodeAttention(raw json.RawMessage) []AccountAttention {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var kept []AccountAttention
	for _, item := range items {
		var parsed AccountAttention
		if json.Unmarshal(item, &parsed) != nil {
			continue
		}
		kept = append(kept, parsed)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

var harnessCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RecoveryFix is the single repair mapping for account and harness reports:
//   - repin: Claude pin and dependency problems; repin replaces the shared
//     Node/SDK pins, including missing ones.
//   - add_harness: the same problems for other harnesses. For a connected
//     account it renews only that account's blocked interpreter pin.
//   - login: the vendor's normal sign-in.
//   - restart: resume setup after restoring the approved launcher, executable
//     or profile permissions. Service ownership stays with its owner,
//     including Nix/Home Manager.
func RecoveryFix(harness, reason string) HarnessFix {
	switch harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return HarnessFix{}
	}
	switch reason {
	case "binding_missing":
		return HarnessFix{FixAddHarness, "aeon-agentd add-harness --harness " + harness}
	case "dependency_invalid", PinMissing, PinPartial, PinDrifted, PinInvalid, PinUnsafe:
		if harness == "claude" {
			return HarnessFix{FixRepin, "aeon-agentd repin --harness claude"}
		}
		return HarnessFix{FixAddHarness, "aeon-agentd add-harness --harness " + harness}
	case "login_required":
		command := harness + " login"
		switch harness {
		case "claude":
			command = "claude auth login"
		case "cursor":
			command = "cursor-agent login"
		case "pi":
			// pi signs in from its own prompt with /login.
			command = "pi"
		}
		return HarnessFix{FixLogin, command}
	case "harness_failed", "cli_unavailable", "profile_permissions", "probe_timeout", "probe_failed", "capacity_timeout":
		return HarnessFix{FixRestart, "aeon-agentd setup"}
	}
	return HarnessFix{}
}

// KnownHarnessState reports the states of the legacy harness_statuses map.
// Other bounded state tokens travel only in harness_details.
func KnownHarnessState(state string) bool {
	switch state {
	case "ready", "blocked", "login_required", "checking", "draining":
		return true
	}
	return false
}

// HarnessReport retains unknown code tokens so newer daemons remain diagnosable.
// Callers scope reports to enrolled harnesses and check legacy-state agreement.
func HarnessReport(harness, state, reason string) (HarnessDetail, bool) {
	switch harness {
	case "claude", "codex", "cursor", "grok", "pi":
	default:
		return HarnessDetail{}, false
	}
	if !harnessCode.MatchString(state) || reason != "" && !harnessCode.MatchString(reason) {
		return HarnessDetail{}, false
	}
	if state == "login_required" && reason == "" {
		reason = "login_required"
	}
	if (state == "ready" || state == "draining") && reason != "" {
		return HarnessDetail{}, false
	}
	d := HarnessDetail{State: state, Reason: reason}
	switch state {
	case "blocked", "login_required", "checking":
		d.Fix = RecoveryFix(harness, reason)
	}
	return d, true
}

// PartialAttention keeps a ready harness's blocked accounts only when they
// are a proper subset of the enrolled accounts. Empty reasons, unknown
// account ids, malformed codes and a list that covers every enrollment are
// dropped. The result is sorted by account id. Accounts past
// AttentionAccountLimit stay in Count and Truncated is set; they are not
// reported as ready.
func PartialAttention(harness string, enrolledIDs []string, state string, items []AccountAttention) AttentionBlock {
	if state != "ready" || len(items) == 0 {
		return AttentionBlock{}
	}
	enrolled := map[string]bool{}
	for _, id := range enrolledIDs {
		if id != "" {
			enrolled[id] = true
		}
	}
	if len(enrolled) < 2 {
		return AttentionBlock{}
	}
	seen := map[string]bool{}
	kept := make([]AccountAttention, 0, len(items))
	for _, item := range items {
		if item.Reason == "" || !enrolled[item.AccountID] || seen[item.AccountID] {
			continue
		}
		if _, ok := HarnessReport(harness, "blocked", item.Reason); !ok {
			continue
		}
		seen[item.AccountID] = true
		kept = append(kept, AccountAttention{AccountID: item.AccountID, Reason: item.Reason})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].AccountID < kept[j].AccountID })
	if len(kept) == 0 || len(kept) >= len(enrolled) {
		return AttentionBlock{}
	}
	block := AttentionBlock{Accounts: kept, Count: len(kept)}
	if len(kept) > AttentionAccountLimit {
		block.Accounts = append([]AccountAttention(nil), kept[:AttentionAccountLimit]...)
		block.Truncated = true
	}
	return block
}

// WithDeclaredTotal keeps a caller's larger proper-subset count even when the
// truncation flag was omitted, and keeps an explicit truncation flag even
// when the count was omitted. A count that meets or exceeds enrollment is
// ignored and does not shrink the validated list. A list of exactly
// legacyAttentionCap names and no count stays uncertain, because an older
// daemon stopped there. A matching count, including five names, stays
// complete. Truncation already applied by PartialAttention is left in place.
func (b AttentionBlock) WithDeclaredTotal(count int, truncated bool, enrolled int) AttentionBlock {
	if len(b.Accounts) == 0 {
		return b
	}
	if count > b.Count && count < enrolled {
		b.Count = count
		b.Truncated = true
		return b
	}
	if truncated {
		b.Truncated = true
		return b
	}
	if count == 0 && len(b.Accounts) == legacyAttentionCap {
		b.Truncated = true
	}
	return b
}

// ResolveAttention validates a reported list, then applies a declared total
// and legacy-cap uncertainty.
func ResolveAttention(harness, state string, enrolledIDs []string, items []AccountAttention, declaredCount int, declaredTruncated bool) AttentionBlock {
	enrolled := map[string]bool{}
	for _, id := range enrolledIDs {
		if id != "" {
			enrolled[id] = true
		}
	}
	return PartialAttention(harness, enrolledIDs, state, items).WithDeclaredTotal(declaredCount, declaredTruncated, len(enrolled))
}

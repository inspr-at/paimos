// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/inspr-at/paimos/backend/safetext"
)

const maxAccountChoiceLabel = 48

// ValidAccountChoiceLabel reports whether value may be advertised as a
// non-secret named-account display label. The grammar is stricter than
// opaque account keys: no colon, at most 48 characters, and never secret-like.
func ValidAccountChoiceLabel(value string) bool {
	return AccountChoiceLabelCause(value) == ""
}

// AccountChoiceLabelCause names the contract violation without echoing value.
func AccountChoiceLabelCause(value string) string {
	if value == "" {
		return "empty"
	}
	if value != strings.TrimSpace(value) {
		return "surrounding whitespace"
	}
	if safetext.ContainsSecretLike(value) {
		return "secret-like value"
	}
	if len(value) > maxAccountChoiceLabel {
		return "exceeds 48 characters"
	}
	if strings.Contains(value, ":") {
		return "colon not allowed"
	}
	if !workspaceLabel.MatchString(value) {
		return "disallowed characters"
	}
	if validAccount(value) || value == "unknown" || value == "local_probe" {
		return "reserved account class"
	}
	return ""
}

// DisplayLabelForAccountKey returns a stable, contract-valid, non-secret label
// for an opaque key. When the key already satisfies label grammar and avoids
// reserved names, it is used unchanged so ordinary keys keep a human-controlled
// appearance. Otherwise a derived label is produced; the key itself is never
// rewritten.
func DisplayLabelForAccountKey(key string, reserved []string) string {
	blocked := reservedSet(reserved)
	if ValidAccountChoiceLabel(key) && !blocked[key] {
		return key
	}
	if label := sanitizeAccountKeyAsLabel(key); ValidAccountChoiceLabel(label) && !blocked[label] {
		return label
	}
	sum := sha256.Sum256([]byte("paimos-lifecycle-account-choice-label-v1\x00" + key))
	digest := hex.EncodeToString(sum[:])
	for n := 8; n <= 40 && n <= len(digest); n += 2 {
		label := "Account " + digest[:n]
		if ValidAccountChoiceLabel(label) && !blocked[label] {
			return label
		}
	}
	return ""
}

func sanitizeAccountKeyAsLabel(key string) string {
	label := strings.ReplaceAll(key, ":", "-")
	if len(label) > maxAccountChoiceLabel {
		label = label[:maxAccountChoiceLabel]
	}
	return label
}

func reservedSet(reserved []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range reserved {
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// AccountChoiceDimensionCollisionCause reports why an advertised key or label
// collides with another registration dimension. It does not echo value.
func AccountChoiceDimensionCollisionCause(value, generation, host, class string, profiles []Profile) string {
	if value == "" {
		return ""
	}
	if value == generation {
		return "collides with runtime generation"
	}
	if value == host {
		return "collides with runtime host"
	}
	if value == class {
		return "collides with account class"
	}
	for _, profile := range profiles {
		if value == profile.ID || value == profile.Version {
			return "collides with profile"
		}
	}
	return ""
}

func ValidateAdvertisedAccounts(in Registration) error {
	if len(in.Accounts) == 0 {
		if in.SchemaVersion != 0 && in.SchemaVersion != RuntimeSchemaV1 {
			return ErrInvalid
		}
		return nil
	}
	if in.SchemaVersion != AccountChoiceSchemaV2 || len(in.Accounts) > maxAdvertisedAccounts {
		return ErrInvalid
	}
	keys, labels := map[string]bool{}, map[string]bool{}
	for _, choice := range in.Accounts {
		if !validAccountKey(choice.Key) || !ValidAccountChoiceLabel(choice.Label) || keys[choice.Key] || labels[choice.Label] {
			return ErrInvalid
		}
		if AccountChoiceDimensionCollisionCause(choice.Key, in.Generation, in.Host, in.AccountLabel, in.Profiles) != "" {
			return ErrInvalid
		}
		if AccountChoiceDimensionCollisionCause(choice.Label, in.Generation, in.Host, in.AccountLabel, in.Profiles) != "" {
			return ErrInvalid
		}
		keys[choice.Key] = true
		labels[choice.Label] = true
	}
	for _, choice := range in.Accounts {
		if choice.Label != choice.Key && keys[choice.Label] {
			return ErrInvalid
		}
	}
	return nil
}

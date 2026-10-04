// SPDX-License-Identifier: AGPL-3.0-only

// Package scopecode encodes a public permission proposal, never a credential.
package scopecode

import (
	"errors"
	"fmt"
	"hash/crc32"
	"regexp"
	"slices"
	"strings"
)

const Prefix = "aeon-scopes:v1:"
const MaxScopes = 256
const MaxCodeBytes = 32768

var scopeID = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
var ErrInvalid = errors.New("invalid scope code; copy the complete aeon-scopes:v1 code")

// Encode sorts and deduplicates explicit identifiers. Their meaning does not
// depend on registry positions, so adding a permission never changes old codes.
// Unknown and person-only identifiers can be proposed, but confer no authority.
func Encode(scopes []string) (string, error) {
	if len(scopes) > MaxScopes {
		return "", ErrInvalid
	}
	keys := make([]string, 0, len(scopes))
	for _, key := range scopes {
		key = strings.ReplaceAll(strings.TrimSpace(key), ":", ".")
		if len(key) > 128 || !scopeID.MatchString(key) {
			return "", ErrInvalid
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	groups := []string{}
	last := ""
	for _, key := range keys {
		resource, action, _ := strings.Cut(key, ".")
		if resource == last {
			groups[len(groups)-1] += "+" + action
		} else {
			groups = append(groups, key)
			last = resource
		}
	}
	text := Prefix + strings.Join(groups, ",")
	code := fmt.Sprintf("%s:%08x", text, crc32.ChecksumIEEE([]byte(text)))
	if len(code) > MaxCodeBytes {
		return "", ErrInvalid
	}
	return code, nil
}

// Decode accepts surrounding clipboard whitespace, verifies the checksum and
// canonical form, and leaves registry/role/creator validation to the dialog.
func Decode(code string) ([]string, error) {
	if len(code) > MaxCodeBytes {
		return nil, ErrInvalid
	}
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, Prefix) {
		return nil, ErrInvalid
	}
	payload, checksum, ok := strings.Cut(strings.TrimPrefix(code, Prefix), ":")
	if !ok || len(checksum) != 8 || checksum != fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(Prefix+payload))) {
		return nil, ErrInvalid
	}
	scopes := []string{}
	if payload != "" {
		for _, group := range strings.Split(payload, ",") {
			resource, actions, ok := strings.Cut(group, ".")
			if !ok {
				return nil, ErrInvalid
			}
			for _, action := range strings.Split(actions, "+") {
				scopes = append(scopes, resource+"."+action)
				if len(scopes) > MaxScopes {
					return nil, ErrInvalid
				}
			}
		}
	}
	canonical, err := Encode(scopes)
	if err != nil || canonical != code {
		return nil, ErrInvalid
	}
	return scopes, nil
}

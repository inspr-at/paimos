// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package pharoslink defines the deliberately narrow contract between a
// Paimos work record and a Pharos host-action request. It validates only the
// opaque identifier syntax; Paimos never calls Pharos or provisions a host.
package pharoslink

import (
	"fmt"

	"github.com/inspr-at/paimos/backend/safetext"
)

const (
	MinRequestIDBytes = 8
	MaxRequestIDBytes = 128
)

// ValidateRequestID accepts the same conservative identifier alphabet used by
// Pharos host-action IDs. Empty means "no link" and is valid so updates can
// clear the optional field.
func ValidateRequestID(value string) error {
	if value == "" {
		return nil
	}
	if len(value) < MinRequestIDBytes || len(value) > MaxRequestIDBytes {
		return fmt.Errorf("must be %d-%d ASCII characters", MinRequestIDBytes, MaxRequestIDBytes)
	}
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return fmt.Errorf("may contain only ASCII letters, digits, hyphens, and underscores")
	}
	if safetext.ContainsSecretLike(value) {
		return fmt.Errorf("must not contain secret-like material")
	}
	return nil
}

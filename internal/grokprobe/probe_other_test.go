// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin

package grokprobe

import "testing"

func TestNonDarwinGrokIsUnsupported(t *testing.T) {
	if _, err := Probe(t.Context(), Binding{}); err == nil {
		t.Fatal("non-Darwin probe accepted")
	}
}

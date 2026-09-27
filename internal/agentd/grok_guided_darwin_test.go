// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"testing"

	"github.com/inspr-at/paimos/internal/grokprobe"
)

func TestGrokGuidedPinMatchesNativeAdapter(t *testing.T) {
	for _, variant := range []string{"npm-grok-1.0.30", "source-xai-grok-pager-1.0.32"} {
		name, digest, err := grokprobe.Variant(variant)
		native, nativeErr := grokVariant(variant)
		if err != nil || nativeErr != nil || native.name != name || native.digest != digest {
			t.Fatal("guided and native binary qualifications diverged")
		}
	}
}

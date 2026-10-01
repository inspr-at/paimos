// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestModelInvalidRequiresExplicitVendorEvidence(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{`{"code":"model_not_found","message":"unavailable"}`, true},
		{`{"message":"The model gpt-example does not exist"}`, true},
		{`{"message":"Unsupported model"}`, true},
		{`{"code":400,"message":"Invalid input"}`, false},
		{`{"message":"Model quota exhausted"}`, false},
		{`{"message":"Model not available due to capacity"}`, false},
		{`malformed`, false},
	} {
		if got := explicitModelInvalid(json.RawMessage(test.raw)); got != test.want {
			t.Fatalf("%s: %v", test.raw, got)
		}
	}
	if !errors.Is(modelStartError("launch refused", errModelInvalid), errModelInvalid) {
		t.Fatal("lost typed evidence")
	}
	if errors.Is(modelStartError("launch refused", errors.New("HTTP 400")), errModelInvalid) {
		t.Fatal("generic error became invalid model")
	}
}

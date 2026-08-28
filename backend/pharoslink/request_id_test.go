package pharoslink

import (
	"strings"
	"testing"
)

func TestValidateRequestID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "empty clears link", value: "", valid: true},
		{name: "dispatch request", value: "pharos-create-csb1-1787912345000-1", valid: true},
		{name: "host action", value: "action-reboot-csb1-1787912345-2", valid: true},
		{name: "underscore", value: "request_123", valid: true},
		{name: "too short", value: "abc-123", valid: false},
		{name: "too long", value: strings.Repeat("a", MaxRequestIDBytes+1), valid: false},
		{name: "whitespace", value: "pharos request 123", valid: false},
		{name: "url is not an id", value: "https://pharos.invalid/requests/123", valid: false},
		{name: "secret-shaped free text", value: "Bearer sk-secret-value", valid: false},
		{name: "secret-shaped identifier", value: "sk_test_abcdefghijklmnopqrstuvwxyz", valid: false},
		{name: "non ascii", value: "pharos-äction", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRequestID(tc.value)
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateRequestID(%q) error=%v, valid=%v", tc.value, err, tc.valid)
			}
		})
	}
}

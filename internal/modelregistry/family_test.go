// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestNormalizeAuthorFamily(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"claude", "anthropic"}, {"codex", "openai"}, {"grok", "xai"},
		{"anthropic", "anthropic"}, {"openai", "openai"}, {"xai", "xai"}, {"cursor", "cursor"},
		{"", ""}, {"  ", ""}, {" codex ", "openai"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeAuthorFamily(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeAuthorFamily(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestAuthorFamilyRejections(t *testing.T) {
	for _, input := range []string{"pi", "unknown"} {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizeAuthorFamily(input)
			if err == nil || got != "" {
				t.Fatalf("invalid family accepted: %q, %v", got, err)
			}
			for _, accepted := range []string{"openai", "anthropic", "xai", "cursor", "codex", "claude", "grok"} {
				if !strings.Contains(err.Error(), accepted) {
					t.Errorf("error %q omits accepted value %q", err, accepted)
				}
			}
			if input == "pi" && (!strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "pass the model family")) {
				t.Fatalf("pi must request an explicit family: %v", err)
			}
			// Both entry points reject invalid families before accessing a DB.
			_, resolveErr := resolveRole(context.Background(), nil, resolveQuery{Role: "review-gate", AuthorFamily: input}, time.Now())
			_, reviewErr := ResolveReview(context.Background(), nil, tenant.Principal{}, input, "", time.Now())
			for _, serverErr := range []error{resolveErr, reviewErr} {
				var he *httpError
				if !errors.As(serverErr, &he) || he.status != http.StatusBadRequest || he.msg != err.Error() {
					t.Errorf("server error = %v; want HTTP 400 with %q", serverErr, err)
				}
			}
		})
	}
}

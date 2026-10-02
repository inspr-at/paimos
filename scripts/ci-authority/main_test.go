// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"testing"
)

func TestAuthorityCLIHasNoPublicationOrActivationMode(t *testing.T) {
	for _, args := range [][]string{nil, {"publish"}, {"activate"}, {"shadow", "--write"}, {"shadow", "--admission", "/candidate/admission"}, {"observe", "--attempt", "2"}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("unsafe/incomplete CLI mode accepted %v", args)
		}
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	if err := run(context.Background(), []string{"shadow"}, io.Discard); err == nil {
		t.Fatal("external authority ran in Actions")
	}
}

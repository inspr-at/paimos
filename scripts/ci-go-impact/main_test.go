// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"testing"
)

func TestCLIHasOnlyShadowDiagnostics(t *testing.T) {
	for _, args := range [][]string{nil, {"execute"}, {"reuse"}, {"enable"}, {"publish"}, {"shadow", "--enable"}, {"shadow", "unexpected"}, {"shadow", "--plan", "file", "--mirror", "relative"}} {
		var out bytes.Buffer
		if run(context.Background(), args, &out) == nil {
			t.Fatalf("unsafe invocation accepted %v", args)
		}
		if out.Len() != 0 {
			t.Fatal("failed invocation emitted a selection")
		}
	}
}

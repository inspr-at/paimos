// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"testing"
)

func TestCLIHasNoExecutionOrPublicationMode(t *testing.T) {
	for _, args := range [][]string{nil, {"execute"}, {"reuse"}, {"publish"}, {"enable"}, {"shadow", "--enable"}, {"shadow", "unexpected"}, {"digest", "--mirror", "relative"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatalf("unsafe or unresolved invocation admitted: %v", args)
		}
		if out.Len() != 0 {
			t.Fatal("failed invocation emitted a plan")
		}
	}
}

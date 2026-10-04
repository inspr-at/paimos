// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"testing"
)

func TestVerifyCommandRequiresExplicitExistingAccount(t *testing.T) {
	for _, args := range [][]string{{}, {"--account", "x", "unexpected"}, {"--unknown"}} {
		var out bytes.Buffer
		opened := false
		err := requestVerificationApproval(args, &out, func(context.Context, string) error { opened = true; return nil })
		if err == nil || opened || out.Len() != 0 {
			t.Fatal("invalid request reached approval browser")
		}
	}
}

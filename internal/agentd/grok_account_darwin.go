// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"context"
	"errors"
	"runtime"

	"github.com/inspr-at/paimos/internal/grokprobe"
)

type grokAuthSnapshot = grokprobe.Snapshot

// The shared trusted probe owns secure vendor-auth parsing and the fixed
// userinfo exchange. Existing expected-principal calls keep their fence.
func readGrokAuth(path, expectedPrincipal string) (grokAuthSnapshot, string, string, error) {
	if runtime.GOARCH != "arm64" {
		return grokAuthSnapshot{}, "", "", errors.New("native Grok binary is not qualified for this architecture")
	}
	return grokprobe.ReadAuth(path, expectedPrincipal)
}
func verifyGrokUserInfo(ctx context.Context, bearer, expectedSub string) error {
	return grokprobe.VerifyUserInfo(ctx, bearer, expectedSub)
}
func verifyGrokAuthUnchanged(path, expectedPrincipal string, before grokAuthSnapshot) error {
	return grokprobe.AuthUnchanged(path, expectedPrincipal, before)
}

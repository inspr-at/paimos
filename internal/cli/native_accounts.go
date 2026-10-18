// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// resolveNativeAccount is the local helper for I2's native-start wiring. Actor
// and policy must be supplied by current authorization, not CLI account labels.
// No environment is printed or installed in the parent. Existing use/confined
// launches keep their current behavior; there is no account fallback here.
func resolveNativeAccount(ctx context.Context, profiles nativecontract.ProfileResolver, actor nativecontract.Actor,
	start nativecontract.LocalStart, policy nativecontract.Policy, revision int64) (nativecontract.LocalProfile, error) {
	var empty nativecontract.LocalProfile
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if profiles == nil || start.Intent.Profile != "native" || actor.TenantID == "" || actor.OwnerPersonID == "" ||
		actor.GrantID != policy.GrantID || actor.GrantRevision != policy.GrantRevision ||
		!validUUID(start.Intent.AccountID) || start.AccountKey == "" || len(start.AccountKey) > 128 ||
		start.Intent.WorkspaceID == "" || len(start.Intent.WorkspaceID) > 128 || revision < 0 || revision > 1<<53-1 ||
		len(start.Cwd) > 4096 || strings.ContainsAny(start.Cwd, "\x00\r\n") || !filepath.IsAbs(start.Cwd) || filepath.Clean(start.Cwd) != start.Cwd {
		return empty, errors.New("native account requires an exact authorized local start")
	}
	profile, err := profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: actor.TenantID, OwnerPersonID: actor.OwnerPersonID,
		AccountID: start.Intent.AccountID, AccountKey: start.AccountKey, WorkspaceID: start.Intent.WorkspaceID,
		Harness: start.Intent.Harness, Policy: policy, BindingRevision: revision})
	if err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if profile.AccountID != start.Intent.AccountID || profile.ProfileBindingRevision != revision ||
		!filepath.IsAbs(profile.WorkspaceRoot) || filepath.Clean(profile.WorkspaceRoot) != profile.WorkspaceRoot {
		return empty, errors.New("native account resolver returned another binding")
	}
	// Until the native wiring owner supplies a physical subdirectory proof, a
	// start uses the exact approved workspace root, never a symlinked child cwd.
	if start.Cwd != profile.WorkspaceRoot {
		return empty, errors.New("native start requires the approved physical workspace root")
	}
	profile.Environment = append([]string(nil), profile.Environment...)
	return profile, nil
}

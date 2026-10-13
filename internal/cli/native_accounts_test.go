// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type nativeAccountResolver struct {
	request nativecontract.ProfileRequest
	profile nativecontract.LocalProfile
	err     error
	calls   int
}

func (r *nativeAccountResolver) Resolve(ctx context.Context, q nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	r.calls++
	r.request = q
	return r.profile, r.err
}

func TestNativeAccountStartBindsAuthorizedProfile(t *testing.T) {
	accountID := "10000000-0000-4000-8000-000000000001"
	actor := nativecontract.Actor{TenantID: "tenant", OwnerPersonID: "owner", GrantID: "grant", GrantRevision: 3}
	policy := nativecontract.Policy{GrantID: actor.GrantID, GrantRevision: actor.GrantRevision}
	start := nativecontract.LocalStart{Intent: nativecontract.StartIntent{Profile: "native", AccountID: accountID, WorkspaceID: "project", Harness: "claude"}, AccountKey: "enrolled", Cwd: "/work/project"}
	profile := nativecontract.LocalProfile{AccountID: accountID, ProfileBindingRevision: 7, WorkspaceRoot: "/work/project", Environment: []string{"HOME=/owner"}}
	resolver := &nativeAccountResolver{profile: profile}
	got, err := resolveNativeAccount(t.Context(), resolver, actor, start, policy, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := nativecontract.ProfileRequest{TenantID: actor.TenantID, OwnerPersonID: actor.OwnerPersonID, AccountID: accountID,
		AccountKey: "enrolled", WorkspaceID: "project", Harness: "claude", Policy: policy, BindingRevision: 7}
	if resolver.calls != 1 || !reflect.DeepEqual(resolver.request, want) || !reflect.DeepEqual(got, profile) {
		t.Fatal("CLI did not bind the exact authorized account")
	}
	got.Environment[0] = "HOME=/changed"
	if resolver.profile.Environment[0] != "HOME=/owner" {
		t.Fatal("CLI shares the resolver's mutable environment")
	}
	for _, tc := range []struct {
		name   string
		change func(*nativecontract.LocalStart, *nativecontract.Policy)
	}{
		{"confined branch", func(s *nativecontract.LocalStart, p *nativecontract.Policy) { s.Intent.Profile = "confined" }},
		{"another grant", func(s *nativecontract.LocalStart, p *nativecontract.Policy) { p.GrantID = "another" }},
		{"stale grant", func(s *nativecontract.LocalStart, p *nativecontract.Policy) { p.GrantRevision-- }},
		{"no enrollment", func(s *nativecontract.LocalStart, p *nativecontract.Policy) { s.AccountKey = "" }},
		{"invalid account", func(s *nativecontract.LocalStart, p *nativecontract.Policy) { s.Intent.AccountID = "label" }},
		{"relative workspace", func(s *nativecontract.LocalStart, p *nativecontract.Policy) { s.Cwd = "project" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := start, policy
			tc.change(&s, &p)
			r := &nativeAccountResolver{profile: profile}
			got, err := resolveNativeAccount(t.Context(), r, actor, s, p, 7)
			if err == nil || r.calls != 0 || got.AccountID != "" {
				t.Fatal("unauthorized start reached account resolution")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*nativeAccountResolver)
	}{
		{"another account", func(r *nativeAccountResolver) { r.profile.AccountID = "another" }},
		{"stale profile", func(r *nativeAccountResolver) { r.profile.ProfileBindingRevision-- }},
		{"another workspace", func(r *nativeAccountResolver) { r.profile.WorkspaceRoot = "/work/other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &nativeAccountResolver{profile: profile}
			tc.change(r)
			got, err := resolveNativeAccount(t.Context(), r, actor, start, policy, 7)
			if err == nil || r.calls != 1 || got.AccountID != "" {
				t.Fatal("foreign resolver binding became launchable")
			}
		})
	}
	wantErr := errors.New("fixture identity mismatch")
	resolver = &nativeAccountResolver{err: wantErr}
	if _, err := resolveNativeAccount(t.Context(), resolver, actor, start, policy, 7); !errors.Is(err, wantErr) || resolver.calls != 1 {
		t.Fatal("failed account resolution tried fallback or hid refusal")
	}
}

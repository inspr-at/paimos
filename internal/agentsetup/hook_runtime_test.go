// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeHookReceiptCannotAuthenticateUnsignedImage(t *testing.T) {
	f := newHookFixture(t)
	if got := f.apply(t, false); got != "" {
		t.Fatal(got)
	}
	// The fixture's install verifier is deliberately permissive. All receipt,
	// digest, inode and owned-settings checks pass for this unsigned shell file.
	// Runtime must use the independent release verifier, not that receipt.
	pin, config, err := readUserHookIdentity(f.home, testComputer, "claude")
	if !errors.Is(err, errHookArtifact) || pin != (HookPin{}) || config != "" {
		t.Fatal("receipt authenticated an unsigned runtime image")
	}
}

func TestRuntimeHookReauthenticatesReceiptAndLoadedImage(t *testing.T) {
	f := newHookFixture(t)
	if got := f.apply(t, false); got != "" {
		t.Fatal(got)
	}
	calls := 0
	verify := func(ctx context.Context, path, digest string) error {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2500*time.Millisecond {
			t.Fatal("unbounded runtime verification")
		}
		if !strings.HasPrefix(path, filepath.Join(f.home, ".local", "share", "aeon", "hooks", "aeon-")) {
			t.Fatal("runtime verified the source instead of the loaded pin")
		}
		return f.installer.authenticate(ctx, path, digest)
	}
	pin, config, err := readUserHookIdentityVerified(t.Context(), f.home, testComputer, "claude", verify)
	if err != nil || config == "" || calls != 1 {
		t.Fatal("runtime skipped independent verification", err)
	}
	if pin.matchesLoadedImage(t.Context(), pin.Device, pin.Inode, verify) != nil || calls != 2 {
		t.Fatal("loaded peer skipped verification")
	}
	if pin.matchesLoadedImage(t.Context(), pin.Device, pin.Inode+1, verify) == nil || calls != 2 {
		t.Fatal("wrong loaded image reached the verifier")
	}
	// Structural identity is unchanged, but the signer can no longer be
	// authenticated. Neither a previous success nor a receipt is authority.
	reject := func(context.Context, string, string) error { return errors.New(hookCanary) }
	if err := pin.matchesLoadedImage(t.Context(), pin.Device, pin.Inode, reject); !errors.Is(err, errHookArtifact) {
		t.Fatal("signer rejection was ignored or leaked")
	}
	if pin.MatchesLoadedImage(pin.Device, pin.Inode) {
		t.Fatal("public peer check accepted an unsigned fixture")
	}
}

func TestRuntimeHookChangesDuringVerification(t *testing.T) {
	for _, what := range []string{"same-bytes-new-inode", "different-bytes", "settings", "cancelled"} {
		t.Run(what, func(t *testing.T) {
			f := newHookFixture(t)
			if got := f.apply(t, false); got != "" {
				t.Fatal(got)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := "artifact_changed"
			concurrent := []byte(`{"editor":"concurrent"}`)
			verify := func(_ context.Context, path, _ string) error {
				switch what {
				case "same-bytes-new-inode", "different-bytes":
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if what == "different-bytes" {
						raw = []byte("substituted")
					}
					if err := os.WriteFile(path+".replacement", raw, 0500); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path+".replacement", path); err != nil {
						t.Fatal(err)
					}
				case "settings":
					want = "settings_changed"
					if err := os.WriteFile(f.settings, concurrent, 0640); err != nil {
						t.Fatal(err)
					}
				case "cancelled":
					want = "artifact_untrusted"
					cancel()
				}
				return nil
			}
			pin, config, err := readUserHookIdentityVerified(ctx, f.home, testComputer, "claude", verify)
			if err == nil || err.Error() != want || pin != (HookPin{}) || config != "" {
				t.Fatal("concurrent change retained runtime identity", err)
			}
			if what == "settings" {
				after, err := os.ReadFile(f.settings)
				if err != nil || !bytes.Equal(after, concurrent) {
					t.Fatal("runtime overwrote concurrent settings")
				}
			}
		})
	}
}

func TestHookReleaseCeilingRequiresExactReviewedArtifact(t *testing.T) {
	r := hookRelease{VersionScheme: "inspr-calver-3", Version: "261003000000.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: Hash([]byte("reviewed-fixture"))}
	if got, ok := approvedHookRelease(r.SHA256, r.OS, r.Arch, []hookRelease{r}); !ok || got != r {
		t.Fatal("exact reviewed fixture rejected")
	}
	for _, what := range []string{"unreviewed", "other-digest", "other-os", "other-arch", "invalid-scheme", "invalid-version"} {
		t.Run(what, func(t *testing.T) {
			ceiling := []hookRelease{r}
			digest, goos, arch := r.SHA256, r.OS, r.Arch
			switch what {
			case "unreviewed":
				ceiling = approvedHookReleases()
			case "other-digest":
				digest = Hash([]byte("unreviewed"))
			case "other-os":
				goos = "other"
			case "other-arch":
				arch = "other"
			case "invalid-scheme":
				ceiling[0].VersionScheme = "unknown"
			case "invalid-version":
				ceiling[0].Version = "260231000000.0.0"
			}
			if _, ok := approvedHookRelease(digest, goos, arch, ceiling); ok {
				t.Fatal("release ceiling widened")
			}
		})
	}
}

func TestHookAuthenticationRequiresSignerAndReleaseCeiling(t *testing.T) {
	r := hookRelease{VersionScheme: "inspr-calver-3", Version: "261003000000.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: Hash([]byte("reviewed-fixture"))}
	for _, tc := range []struct {
		name             string
		reviewed, signer bool
	}{
		{"approved", true, true}, {"unreviewed-signed", false, true}, {"wrong-signer", true, false}, {"unreviewed-unsigned", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ceiling []hookRelease
			if tc.reviewed {
				ceiling = []hookRelease{r}
			}
			calls := 0
			verify := func(_ context.Context, path string, got hookRelease) error {
				calls++
				if tc.reviewed && (got != r || path != "/fixture/aeon") {
					t.Fatal("signer verified a different release")
				}
				if !tc.signer {
					return errors.New(hookCanary)
				}
				return nil
			}
			err := authenticateHookRelease(t.Context(), "/fixture/aeon", r.SHA256, r.OS, r.Arch, ceiling, verify)
			if (err == nil) != (tc.reviewed && tc.signer) || (err != nil && !errors.Is(err, errHookArtifact)) {
				t.Fatal("authentication bypassed independent approval")
			}
			if (calls == 1) != tc.reviewed {
				t.Fatal("unreviewed artifact reached signer")
			}
		})
	}
}

func TestHookManifestMustMatchReviewedReleaseMetadata(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := hookRelease{VersionScheme: "inspr-calver-3", Version: "261003000000.0.0", OS: "linux", Arch: "arm64", SHA256: Hash([]byte("reviewed"))}
	for _, what := range []string{"approved", "newer", "missing-version", "invalid-date", "unknown-scheme", "older-scheme", "unknown-field", "duplicate-field", "oversize"} {
		t.Run(what, func(t *testing.T) {
			m := map[string]string{"Schema": "aeon.hook-release.v1", "Artifact": "aeon-cli", "VersionScheme": r.VersionScheme, "Version": r.Version, "OS": r.OS, "Arch": r.Arch, "SHA256": r.SHA256}
			switch what {
			case "newer":
				m["Version"] = "261004000000.0.0"
			case "missing-version":
				delete(m, "Version")
			case "invalid-date":
				m["Version"] = "260231000000.0.0"
			case "unknown-scheme":
				m["VersionScheme"] = "unknown"
			case "older-scheme":
				m["VersionScheme"] = "inspr-calendar-v2"
			case "unknown-field":
				m["Config"] = hookCanary
			case "oversize":
				m["Config"] = strings.Repeat("x", 8193)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if what == "duplicate-field" {
				raw = append(raw[:len(raw)-1], []byte(`,"Version":"`+r.Version+`"}`)...)
			}
			err = verifyHookManifest(raw, ed25519.Sign(private, raw), pub, r)
			if (err == nil) != (what == "approved") {
				t.Fatal("signed manifest escaped the independent release ceiling", err)
			}
		})
	}
}

func TestHookPublicEvidencePinPreservesExactBytes(t *testing.T) {
	f := newHookFixture(t)
	store, err := OpenStore(filepath.Join(f.home, "pins"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	name := "fixture.manifest.json"
	for i := 0; i < 2; i++ {
		if err := pinHookPublicFile(store, f.source+".manifest.json", name, 8192); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Read(name, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.source+".manifest.json", []byte("substitution"), 0600); err != nil {
		t.Fatal(err)
	}
	if pinHookPublicFile(store, f.source+".manifest.json", name, 8192) == nil {
		t.Fatal("owned evidence overwritten")
	}
	after, err := store.Read(name, 8192)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("public evidence lost")
	}
}

func TestHookReportsDoNotBlockNewHarnessEnrollments(t *testing.T) {
	e, api, _, options, _ := engineFixture(t)
	approveFixture(t, e, api, options)
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	originalHarness := s.Candidates[0].Candidate.Harness
	for _, harness := range []string{"gemini", "opencode"} {
		c := s.Candidates[0]
		c.Candidate.Harness = harness
		s.Candidates = append(s.Candidates, c)
		s.View.Enrollments = append(s.View.Enrollments, Enrollment{Harness: harness, State: "connected", AccountID: harness})
	}
	if err := e.setupHooks(t.Context(), s, false); err != nil {
		t.Fatal(err)
	}
	if len(s.HookCapabilities) != 1 || s.HookCapabilities[0].Harness != originalHarness || s.HookCapabilities[0].Blocker != "feature_disabled" {
		t.Fatal("unsupported hook report disrupted normal pairing", s.HookCapabilities)
	}
}

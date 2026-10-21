// SPDX-License-Identifier: AGPL-3.0-only

package nativequalification_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativequalification"
)

func waitEvent(t *testing.T, events <-chan agentd.AdapterEvent, match func(agentd.AdapterEvent) bool) agentd.AdapterEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		select {
		case e := <-events:
			if match(e) {
				return e
			}
		case <-ctx.Done():
			t.Fatal("offline SDK did not emit required evidence")
		}
	}
}

// Risk: native persistence imports ambient hooks/tools/credentials or expands
// confined/routine authority. The real P1a bridge runs only a pinned offline SDK.
func TestNativeLeadPinnedSDKPolicyBoundary(t *testing.T) {
	c, identity, node := nativequalification.FixtureForTest(t)
	if nativequalification.DigestForTest(nativequalification.SDKBytesForTest()) != nativequalification.SDKDigestForTest() {
		t.Fatal("offline SDK fixture pin changed")
	}
	if _, err := nativequalification.New(t.Context(), c, identity); err != nil {
		t.Fatal(err)
	}
	overlay, err := nativequalification.SessionOverlay(c.AllowedTools)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(overlay.DisabledLegacyHooks, []string{"attached-inbox", "cswap", "duplicate-subagent-registration"}) || len(overlay.SettingSources) != 0 {
		t.Fatal("legacy hooks inherited")
	}
	var settings struct {
		Hooks map[string][]any `json:"hooks"`
	}
	if json.Unmarshal(overlay.Settings, &settings) != nil || len(settings.Hooks) != 7 {
		t.Fatal("legacy session overlay missing")
	}
	for _, bucket := range settings.Hooks {
		if len(bucket) != 0 {
			t.Fatal("legacy hook command retained")
		}
	}
	for _, name := range []string{"AEON_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPS_TEST_SECRET"} {
		t.Setenv(name, "synthetic-qualification-sentinel")
	}
	events := make(chan agentd.AdapterEvent, 128)
	grant := agentd.NativeLeadGrant{TenantID: c.Actor.TenantID, PrincipalID: c.Actor.PrincipalID, OwnerID: c.Actor.OwnerPersonID, ProjectID: c.Spec.Binding.ProjectID, AccountID: c.Spec.Profile.AccountID, SessionID: c.Spec.Binding.HarnessSessionID, DaemonID: c.Spec.Binding.DaemonID, Generation: c.Spec.Binding.DaemonGeneration, PolicyDigest: c.Spec.Policy.ToolPolicyDigest, AccountHomeDigest: nativequalification.DigestForTest([]byte(c.Spec.Profile.AccountHome)), WorkspaceDigest: nativequalification.DigestForTest([]byte(c.Spec.Cwd)), ResumeDigest: nativequalification.DigestForTest([]byte(c.Spec.ResumeRef)), Revision: 1, ExpiresAt: c.Spec.Policy.ExpiresAt, HistoryApproved: true, AllowedTools: c.AllowedTools}
	var revoked atomic.Bool
	var attempts atomic.Int32
	spec := &agentd.NativeLeadSpec{Grant: grant, Resume: c.Spec.ResumeRef, Prompt: "initial", Model: "fixture-model", Effort: "high", Tools: &agentd.RunTools{URL: c.Spec.Tools.URL, Token: strings.Repeat("0", 64)}, Now: c.Now,
		Require: func(context.Context) error {
			if revoked.Load() {
				return nativequalification.ErrGrant
			}
			return nil
		},
		BeforeInput: func(context.Context, string, string) error { attempts.Add(1); return nil }, Observe: func(e agentd.AdapterEvent) { events <- e }}
	adapter := agentd.NewClaudeAdapter(node, c.Spec.Profile.SDK.Path, c.Spec.Profile.Binary.Path, map[string]string{"fixture": c.Spec.Profile.AccountHome})
	adapter.SetExpectedEmails(map[string]string{"fixture": "qualification@example.test"})
	request := agentd.LeadStartRequest{Native: spec, AccountKey: "fixture", Workspace: c.Spec.Cwd, Lifetime: t.Context(), Session: agentd.HarnessSession{ID: grant.SessionID, ProjectID: grant.ProjectID, Harness: agentd.Claude, Lease: strings.Repeat("l", 32)}}
	process, err := adapter.StartLead(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		revoked.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := process.Stop(ctx); err != nil {
			t.Error("offline child cleanup", err)
		}
		if err := process.Wait(); err != nil {
			t.Error("offline child wait", err)
		}
	})
	final := waitEvent(t, events, func(e agentd.AdapterEvent) bool { return e.Chat != nil && e.Chat.SessionUpdate == "final" })
	if final.Chat.Content == nil || final.Chat.Content.Text != "Explicit SDK final." {
		t.Fatal("partial output became final")
	}
	boundary, err := os.ReadFile(filepath.Join(c.Spec.Cwd, "boundary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if json.Unmarshal(boundary, &facts) != nil {
		t.Fatal("bad fixture facts")
	}
	if facts["protocol"] != nativequalification.SDKProtocolForTest() {
		t.Fatal("fixture protocol pin changed")
	}
	for _, fact := range []string{"legacyHooksAbsent", "ambientToolsDenied", "boundToolAllowed", "daemonCredentialAbsent", "persistentNativeOnly", "accountHomePreserved"} {
		if facts[fact] != true {
			t.Fatal("SDK boundary failed", fact)
		}
	}
	if adapter.RoutineExecutionSupported(agentsetup.RoutineGrant{}) {
		t.Fatal("native qualification widened routines")
	}
	inputProcess, ok := process.(interface {
		Input(context.Context, string, string) error
	})
	if !ok {
		t.Fatal("native input port unavailable")
	}
	revoked.Store(true)
	if err := inputProcess.Input(t.Context(), nativequalification.UIDForTest("20"), "revoked input"); !errors.Is(err, nativequalification.ErrGrant) {
		t.Fatal("wrong revoked-input reason", err)
	}
	if attempts.Load() != 0 {
		t.Fatal("revoked input reached write-attempt boundary")
	}
	revoked.Store(false)
	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !adapter.LeadExitConfirmed(process) {
		t.Fatal("SDK close mistaken for owned exit")
	}
}

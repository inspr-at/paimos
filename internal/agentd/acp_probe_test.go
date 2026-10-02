// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func acpProbeExecutable(t *testing.T, body string) string {
	t.Helper()
	root := privateHome(t)
	path := filepath.Join(root, "vendor")
	// Fail if a probe attempts any command besides the quota-neutral version
	// check. In Gemini, an invented positional status command can be a prompt.
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 90\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestACPLauncherProbeFailureCauses(t *testing.T) {
	for _, harness := range []string{Gemini, OpenCode} {
		for _, tc := range []struct {
			name, body, failure string
			timeout             bool
		}{
			{"version only", "printf '1.14.48\\n'", ProbeUnverified, false},
			{"empty", "exit 0", ProbeProtocol, false},
			{"two streams", "echo version; echo diagnostic >&2", ProbeProtocol, false},
			{"overflow", "head -c 4097 /dev/zero", ProbeProtocol, false},
			{"nonzero is not sign-out", "echo 'Not logged in'; exit 1", ProbeLaunchFailed, false},
			{"timeout", "exec /bin/sleep 30", ProbeTimeout, true},
		} {
			t.Run(harness+"/"+tc.name, func(t *testing.T) {
				a := &ACPAdapter{Harness: harness, Path: acpProbeExecutable(t, tc.body), Homes: map[string]string{"local": privateHome(t)}}
				ctx := t.Context()
				if tc.timeout {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
					defer cancel()
				}
				start := time.Now()
				got := a.ProbeStatus(ctx, "local")
				if got.OK || got.Failure != tc.failure {
					t.Fatalf("got %+v, want %s", got, tc.failure)
				}
				if tc.timeout && time.Since(start) > 3*time.Second {
					t.Fatal("probe exceeded operation and cleanup bounds")
				}
			})
		}
	}
}

func TestACPProbeKeepsPairedEnvironmentAndBinding(t *testing.T) {
	for _, harness := range []string{Gemini, OpenCode} {
		t.Run(harness, func(t *testing.T) {
			t.Setenv("NODE_OPTIONS", "fixture-injection")
			t.Setenv("NODE_PATH", "fixture-injection")
			t.Setenv("GEMINI_API_KEY", "fixture-injection")
			t.Setenv("OPENAI_API_KEY", "fixture-injection")
			home := privateHome(t)
			body := "[ -z \"$NODE_OPTIONS$NODE_PATH$GEMINI_API_KEY$OPENAI_API_KEY\" ] || exit 91\n"
			if harness == Gemini {
				body += "[ \"$GEMINI_CLI_HOME\" = \"$HOME\" ] && [ \"$GEMINI_CLI_NO_RELAUNCH\" = true ] || exit 92\n"
			} else {
				body += "[ \"$XDG_CONFIG_HOME\" = \"$HOME/.config\" ] && [ \"$XDG_DATA_HOME\" = \"$HOME/.local/share\" ] || exit 92\n"
			}
			body += "printf '%s' \"$HOME\" > \"$HOME/probe-home\"\necho 1.14.48"
			a := &ACPAdapter{Harness: harness, Path: acpProbeExecutable(t, body), Homes: map[string]string{"local": home}}
			if got := a.ProbeStatus(t.Context(), "local"); got.OK || got.Failure != ProbeUnverified {
				t.Fatal(got)
			}
			raw, err := os.ReadFile(filepath.Join(home, "probe-home"))
			if err != nil || string(raw) != home {
				t.Fatal("probe did not use its paired HOME")
			}
			if got := a.ProbeStatus(t.Context(), "unknown"); got.OK || got.Failure != ProbeLaunchFailed {
				t.Fatal("unknown binding did not fail closed", got)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if got := a.ProbeStatus(ctx, "local"); got.OK || got.Failure != ProbeTimeout {
				t.Fatal("canceled probe is not a sign-out", got)
			}
		})
	}
}

// This fake supplies qualified outcomes to exercise the consumer state machine.
// It is not evidence qualifying a real vendor command or its startup hooks.
type acpProbeOutcomeFixture struct {
	fakeAdapter
	harness string
	status  ProbeStatus
}

func (a *acpProbeOutcomeFixture) Name() string                                    { return a.harness }
func (a *acpProbeOutcomeFixture) Probe(context.Context, string) bool              { return a.status.OK }
func (a *acpProbeOutcomeFixture) ProbeStatus(context.Context, string) ProbeStatus { return a.status }

func TestACPProbeOutcomesGateDispatchAndRecover(t *testing.T) {
	for _, harness := range []string{Gemini, OpenCode} {
		for _, tc := range []struct {
			name, state, reason string
			status              ProbeStatus
			login               bool
		}{
			{"qualified signed in", "ready", "", probeOK, false},
			{"confirmed signed out", "login_required", "login_required", probeAuthFailed, true},
			{"identity mismatch", "blocked", ProbeIdentityMismatch, ProbeStatus{Failure: ProbeIdentityMismatch}, false},
			{"timeout", "blocked", ProbeTimeout, ProbeStatus{Failure: ProbeTimeout}, false},
			{"protocol", "blocked", ProbeProtocol, ProbeStatus{Failure: ProbeProtocol}, false},
			{"launch failed", "blocked", ProbeLaunchFailed, ProbeStatus{Failure: ProbeLaunchFailed}, false},
			{"unqualified", "blocked", ProbeUnverified, ProbeStatus{Failure: ProbeUnverified}, false},
			{"unknown failure", "blocked", "probe_failed", ProbeStatus{Failure: "private diagnostic"}, false},
		} {
			t.Run(harness+"/"+tc.name, func(t *testing.T) {
				s, api, process := testSupervisor(t)
				s.api = &acpHealthAPI{api}
				s.accounts[0].Harness = harness
				api.profile.Harness = harness
				adapter := &acpProbeOutcomeFixture{fakeAdapter{proc: process}, harness, tc.status}
				s.adapters = map[string]Adapter{harness: adapter}
				if err := s.PollOnce(t.Context()); err != nil {
					t.Fatal(err)
				}
				v := s.Lifecycle("")
				d := v.AccountStatuses["account"]
				if v.Ready != tc.status.OK || d.State != tc.state || d.Reason != tc.reason || v.LoginRequired != tc.login || s.accountAvailable("account") != tc.status.OK {
					t.Fatalf("unexpected readiness: %+v", v)
				}
				if !tc.status.OK {
					if err := s.StartRun(t.Context(), api.run); err == nil || api.claims != 0 {
						t.Fatal("failed probe admitted work", err, api.claims)
					}
					adapter.status = probeOK
					if err := s.PollOnce(t.Context()); err != nil {
						t.Fatal(err)
					}
					v = s.Lifecycle("")
					if !v.Ready || v.AccountStatuses["account"].Reason != "" || v.LoginRequired || !s.accountAvailable("account") {
						t.Fatal("fresh qualified success did not clear the failure", v)
					}
				}
				if err := s.StartRun(t.Context(), api.run); err != nil || api.claims != 1 {
					t.Fatal("qualified fixture could not dispatch", err, api.claims)
				}
			})
		}
	}
}

func TestACPFailureReportsPreserveHistoricalContract(t *testing.T) {
	for _, tc := range []struct{ failure, wire string }{
		{ProbeAuthFailed, ProbeAuthFailed},
		// Identity mismatch needs attention, not another sign-in. Only a
		// confirmed sign-out may use the historical auth_failed category.
		{ProbeIdentityMismatch, ProbeUnavailable},
		{ProbeTimeout, ProbeUnavailable},
		{ProbeProtocol, ProbeUnavailable},
		{ProbeLaunchFailed, ProbeUnavailable},
		{ProbeUnverified, ProbeUnavailable},
		{"private diagnostic", ProbeUnavailable},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			bodies := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				bodies <- body
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			remote := NewRemote(server.URL, "fixture")
			if err := remote.ProbeStatus(t.Context(), "account", "daemon", "generation", ProbeStatus{Failure: tc.failure}); err != nil {
				t.Fatal(err)
			}
			body := <-bodies
			if body["available"] != false || body["failure"] != tc.wire {
				t.Fatal("changed historical probe schema", body)
			}
			for key := range body {
				if key != "available" && key != "failure" && key != "daemon_id" && key != "daemon_generation" && key != "host_label" {
					t.Fatal("unexpected uploaded field", key)
				}
			}
			if raw, _ := json.Marshal(body); strings.Contains(string(raw), "private diagnostic") {
				t.Fatal("uploaded private diagnostic")
			}
		})
	}
}

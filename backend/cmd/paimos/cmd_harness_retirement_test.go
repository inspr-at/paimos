// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type retirementRoundTripFunc func(*http.Request) (*http.Response, error)

func (f retirementRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHarnessRetirementWorkerCommandsUseExactLeaseBoundRoutes(t *testing.T) {
	leaseFile := filepath.Join(t.TempDir(), "worker-lease")
	lease := strings.Repeat("A", 43)
	if err := os.WriteFile(leaseFile, []byte(lease+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const session = "11111111-1111-4111-8111-111111111111"
	const retirement = "22222222-2222-4222-8222-222222222222"

	priorTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = priorTransport })
	t.Setenv(envURL, "https://paimos.test")
	t.Setenv(envAPIKey, "test_key")

	for _, tc := range []struct {
		name, command, suffix, response string
		body                            map[string]string
	}{
		{name: "ready", command: "retirement-ready", suffix: "/ready", body: map[string]string{}, response: `{"id":"` + retirement + `","state":"stopping"}`},
		{name: "complete", command: "complete-retirement", suffix: "/complete", body: map[string]string{"outcome": "rejected", "reason": "outcome_unknown"}, response: `{"id":"` + retirement + `","state":"outcome_unknown"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			http.DefaultTransport = retirementRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				body := `[{"id":6,"key":"PAI"}]`
				if request.URL.Path != "/api/projects" {
					wantPath := "/api/projects/6/harness-sessions/" + session + "/retirements/" + retirement + tc.suffix
					if request.Method != http.MethodPost || request.URL.Path != wantPath {
						t.Fatalf("request=%s %s want POST %s", request.Method, request.URL.Path, wantPath)
					}
					if request.Header.Get(harnessWorkerLeaseHeader) != lease || request.Header.Get(agentAttrHeader) != "worker" {
						t.Fatalf("worker authority headers=%v", request.Header)
					}
					var got map[string]string
					if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
						t.Fatal(err)
					}
					if len(got) != len(tc.body) {
						t.Fatalf("body=%v want=%v", got, tc.body)
					}
					for key, value := range tc.body {
						if got[key] != value {
							t.Fatalf("body=%v want=%v", got, tc.body)
						}
					}
					body = tc.response
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(body)), Request: request}, nil
			})

			args := []string{"--json", "harness", tc.command, "--project", "PAI", "--session", session, "--retirement-id", retirement,
				"--agent", "worker", "--worker-lease-file", leaseFile}
			if tc.command == "complete-retirement" {
				args = append(args, "--outcome", "rejected", "--reason", "outcome_unknown")
			}
			if _, _, err := executeCLIForTest(t, args...); err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatalf("requests=%d want project resolve plus retirement mutation", requests)
			}
		})
	}
}

func TestHarnessRetirementCommandsRequireExplicitRetirementID(t *testing.T) {
	for _, command := range []*cobra.Command{harnessRetirementReadyCmd(), harnessCompleteRetirementCmd()} {
		command.SetArgs([]string{"--project", "PAI", "--session", "11111111-1111-4111-8111-111111111111"})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--retirement-id") {
			t.Fatalf("command=%s error=%v", command.Name(), err)
		}
	}
}

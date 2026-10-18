// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/stagehandoff"
)

func TestReporterRoutesDeclareHeaderBeforeAuthentication(t *testing.T) {
	type routeCase struct {
		name, method, path, version string
		mount                       func(*http.ServeMux)
	}
	cases := []routeCase{
		{"review run create", "POST", "/api/nodes/00000000-0000-4000-8000-000000000001/review-runs", reportercontract.ReviewRun, nodes.New(nil, nil).Mount},
		{"review run credential", "POST", "/api/review-runs/00000000-0000-4000-8000-000000000001/credential", reportercontract.ReviewRun, nodes.New(nil, nil).Mount},
		{"review run verdict", "POST", "/api/review-runs/00000000-0000-4000-8000-000000000001/verdict", reportercontract.ReviewRun, nodes.New(nil, nil).Mount},
		{"review evidence provenance", "GET", "/api/nodes/00000000-0000-4000-8000-000000000001/review-evidence", reportercontract.ReviewRun, nodes.New(nil, nil).Mount},
		{"handoff create", "POST", "/api/stage-handoffs", reportercontract.StageHandoffs, stagehandoff.New(nil, nil).Mount},
		{"handoff get", "GET", "/api/stage-handoffs/00000000-0000-4000-8000-000000000001", reportercontract.StageHandoffs, stagehandoff.New(nil, nil).Mount},
		{"evidence", "POST", "/api/stage-handoffs/00000000-0000-4000-8000-000000000001/evidence", reportercontract.StageEvidence, stagehandoff.New(nil, nil).Mount},
		{"result", "POST", "/api/stage-handoffs/00000000-0000-4000-8000-000000000001/result", reportercontract.StageResult, stagehandoff.New(nil, nil).Mount},
		{"admit", "POST", "/api/stage-handoffs/00000000-0000-4000-8000-000000000001/launch/admit", reportercontract.StageLaunch, stagehandoff.New(nil, nil).Mount},
		{"consume", "POST", "/api/stage-handoffs/00000000-0000-4000-8000-000000000001/launch/consume", reportercontract.StageLaunch, stagehandoff.New(nil, nil).Mount},
		{"classic alias", "POST", "/api/stage-handoffs/00000000-0000-4000-8000-000000000001/classic-batch-alias", reportercontract.BaselineBatches, stagehandoff.New(nil, nil).Mount},
		{"classic built", "POST", "/api/projects/00000000-0000-4000-8000-000000000001/baseline-batches/batches/1/built-receipt", reportercontract.BaselineBatches, stagehandoff.New(nil, nil).Mount},
		{"journey", "GET", "/api/projects/00000000-0000-4000-8000-000000000001/journey", reportercontract.Journey, journey.New(nil).Mount},
		{"approvals list", "GET", "/api/approvals", reportercontract.Approvals, approvals.New(nil).Mount},
		{"approvals get", "GET", "/api/approvals/00000000-0000-4000-8000-000000000001", reportercontract.Approvals, approvals.New(nil).Mount},
		{"approvals propose", "POST", "/api/approvals", reportercontract.Approvals, approvals.New(nil).Mount},
		{"approvals decision", "POST", "/api/approvals/00000000-0000-4000-8000-000000000001/decision", reportercontract.Approvals, approvals.New(nil).Mount},
		{"approvals revoke", "POST", "/api/approvals/00000000-0000-4000-8000-000000000001/revoke", reportercontract.Approvals, approvals.New(nil).Mount},
		{"me", "GET", "/api/me", reportercontract.Me, (&auth.Module{}).Mount},
		{"harness status", "GET", "/api/projects/00000000-0000-4000-8000-000000000001/harness-sessions/00000000-0000-4000-8000-000000000002", reportercontract.HarnessSession, harness.New(nil).Mount},
		{"harness heartbeat", "POST", "/api/projects/00000000-0000-4000-8000-000000000001/harness-sessions/00000000-0000-4000-8000-000000000002/heartbeat", reportercontract.HarnessSession, harness.New(nil).Mount},
		{"project lead", "GET", "/api/projects/00000000-0000-4000-8000-000000000001/lead", reportercontract.ProjectLead, harness.New(nil).Mount},
	}
	for _, route := range nativecontract.PublicRoutes() {
		path := route.Path
		for _, name := range []string{"projectId", "ownedSessionId", "sessionId", "approvalId", "id"} {
			path = strings.ReplaceAll(path, "{"+name+"}", "00000000-0000-4000-8000-000000000001")
		}
		cases = append(cases, routeCase{route.OperationID, route.Method, path, reportercontract.NativeSession, nativesessions.NewDisabled().Mount})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			tc.mount(mux)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if got := recorder.Header().Get(reportercontract.Header); got != tc.version {
				t.Fatalf("Aeon-Contract = %q, want %q (status %d)", got, tc.version, recorder.Code)
			}
		})
	}
}

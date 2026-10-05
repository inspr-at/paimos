// SPDX-License-Identifier: AGPL-3.0-only

package authz_test

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/aithema/host"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/business/costunits"
	"github.com/inspr-at/paimos/internal/business/crm"
	"github.com/inspr-at/paimos/internal/business/directory"
	"github.com/inspr-at/paimos/internal/business/hours"
	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/business/quotes/collaboration"
	"github.com/inspr-at/paimos/internal/business/quotes/confirmation"
	publicquotes "github.com/inspr-at/paimos/internal/business/quotes/public"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/decisiondesk"
	"github.com/inspr-at/paimos/internal/deliveryvote"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/features"
	"github.com/inspr-at/paimos/internal/fromclassic"
	"github.com/inspr-at/paimos/internal/greetings"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/imports"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/intake"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/outcomes"
	"github.com/inspr-at/paimos/internal/parentbenefits"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/portal"
	"github.com/inspr-at/paimos/internal/profile"
	"github.com/inspr-at/paimos/internal/projectgroups"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/relations"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/search"
	"github.com/inspr-at/paimos/internal/stagehandoff"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenantbrand"
	"github.com/inspr-at/paimos/internal/themes"
	"github.com/inspr-at/paimos/internal/ticketwork"
	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/inspr-at/paimos/internal/workorders"
)

// This mirrors the coordinator's production module list at the ServeMux
// boundary. Constructors with runtime secrets are represented by their module
// values; Mount itself only registers paths. The source walk catches newly
// registered paths even before the route declaration table is updated.
func TestRealMuxRouteCoverage(t *testing.T) {
	authModule, err := auth.New(auth.Config{Env: "dev", SessionKey: make([]byte, 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	messaging, err := inbox.NewMessaging(nil, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	modules := []httpapi.Module{
		authModule, &tokens.Module{}, &host.Module{}, authz.New(nil), nodes.New(nil, nil), fromclassic.New(nil), tenantbrand.New(nil), modelprovider.New(nil, nil), relations.New(nil),
		events.New(nil), features.New(nil), search.New(nil, nil), views.New(nil), activity.New(nil),
		attachments.New(nil, attachments.Store{}), &greetings.Module{}, knowledge.New(nil),
		projectgroups.New(nil), &releasehistory.Module{}, profile.New(nil, attachments.Store{}), themes.New(nil),
		imports.New(nil), inbox.New(nil), chat.New(nil), messaging, harness.New(nil), rules.New(nil), doctrine.New(nil, doctrine.Options{}), ticketwork.New(nil), outcomes.New(nil), deliveryvote.New(nil), usagedashboard.New(nil), workorders.New(nil), crossreview.New(nil, nil),
		agentruns.New(nil), agentpairing.New(nil, "https://pairing.test", "test"), approvals.New(nil), questions.New(nil), decisiondesk.New(nil), modelregistry.New(nil), agentaccounts.New(nil),
		journey.New(nil), requirements.New(nil), releases.New(nil), recurrences.New(nil), statusautopilot.New(nil), parentbenefits.New(nil, nil), intake.New(nil),
		plugins.New(nil), stagehandoff.New(nil, nil), costunits.New(nil, nil), crm.New(nil, nil),
		&quotes.Module{}, &collaboration.Module{}, &publicquotes.Module{}, &confirmation.Module{}, portal.New(nil, false, nil),
		hours.New(nil, nil), directory.New(nil, nil), &phoneapprovals.Module{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /api/ready", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /api/version", func(http.ResponseWriter, *http.Request) {})
	for _, module := range modules {
		module.Mount(mux)
	}
	patternRE := regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE|HEAD) /api/[^"\n]+)"`)
	variableRE := regexp.MustCompile(`\{[^}]+\}`)
	seen := map[string]bool{}
	err = filepath.WalkDir("..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "route_map.go" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range patternRE.FindAllSubmatch(body, -1) {
			declaration := string(match[1])
			if seen[declaration] {
				continue
			}
			seen[declaration] = true
			method, route, _ := strings.Cut(declaration, " ")
			req := httptest.NewRequest(method, variableRE.ReplaceAllString(route, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), nil)
			_, matched := mux.Handler(req)
			if matched != declaration {
				t.Errorf("%s: real mux matched %q", declaration, matched)
			}
			if _, declared := authz.PermissionForPattern(matched); !declared {
				t.Errorf("%s: no permission declared", declaration)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

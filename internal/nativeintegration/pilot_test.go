// SPDX-License-Identifier: AGPL-3.0-only

package nativeintegration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeintegration"
	"github.com/inspr-at/paimos/internal/tenant"
)

type unreachableStore struct{ nativecontract.Store }

func (unreachableStore) Start(context.Context, nativecontract.Actor, nativecontract.StartIntent) (nativecontract.OperationResult, error) {
	panic("disabled pilot invoked start authority")
}

type unreachableHost struct{ nativecontract.Host }
type unreachableLauncher struct{}

func (unreachableLauncher) Open(context.Context, nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	panic("disabled pilot invoked a vendor launcher")
}

// Risk: installing the native pilot changes default-off behavior, or a label
// and partial ports enable a path with missing original actor/grant authority.
func TestPilotDefaultsOffAndRejectsIncompleteQualification(t *testing.T) {
	pilot, err := nativeintegration.New(nativeintegration.Options{Services: nativecontract.Services{
		Store: unreachableStore{}, Qualified: true}, Host: unreachableHost{}, Launcher: unreachableLauncher{}})
	if err != nil || pilot.HTTP == nil || pilot.Host != nil || pilot.Launcher != nil || pilot.Admission != nil {
		t.Fatalf("default-off pilot retained active runtime ports: %v", err)
	}
	mux := http.NewServeMux()
	pilot.HTTP.Mount(mux)
	p := tenant.Principal{ID: "60000000-0000-4000-8000-000000000001", TenantID: "60000000-0000-4000-8000-000000000002", Kind: tenant.Person}
	for _, route := range nativecontract.PublicRoutes() {
		path := route.Path
		for _, name := range []string{"projectId", "ownedSessionId", "sessionId", "approvalId", "id"} {
			path = strings.ReplaceAll(path, "{"+name+"}", p.ID)
		}
		request := httptest.NewRequest(route.Method, path, nil).WithContext(tenant.WithPrincipal(t.Context(), p))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || response.Body.String() != `{"reason":"feature_disabled","request_id":null,"retryable":false}`+"\n" {
			t.Fatalf("%s did not stay disabled: HTTP %d %s", route.OperationID, response.Code, response.Body.String())
		}
	}
	_, err = nativeintegration.New(nativeintegration.Options{Enabled: true, Services: nativecontract.Services{
		Store: unreachableStore{}, Qualified: true}, Host: unreachableHost{}, Launcher: unreachableLauncher{}, Admission: &nativeintegration.RootAuthority{}})
	var refused *nativecontract.ValidationError
	if !errors.As(err, &refused) || refused.Reason != nativecontract.ReasonQualificationMissing {
		t.Fatalf("incomplete pilot did not refuse qualification_missing: %v", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

type nativePersonAuthorityFixture struct {
	actor nativecontract.Actor
	calls int
}

func (f *nativePersonAuthorityFixture) Authenticate(*http.Request) (nativecontract.Actor, error) {
	f.calls++
	return f.actor, nil
}
func (f *nativePersonAuthorityFixture) WithAccess(context.Context, nativecontract.Actor, nativestream.LocalRequest, func(nativecontract.ValidationContext) error) error {
	return nativeRefusal(nativecontract.ReasonGrantMissing)
}

// Risk: a protected socket bearer or paired-agent identity is promoted to a
// person. Kernel peer provenance and the independent human verifier are BOTH
// required; read/control authority still needs its separate current fence.
func TestNativeLocalSocketRequiresIndependentPersonProof(t *testing.T) {
	person := nativecontract.Actor{TenantID: uuid.NewString(), PrincipalID: uuid.NewString(), PrincipalKind: "person"}
	fake := &nativePersonAuthorityFixture{actor: person}
	allowed := false
	a := &nativeSocketAuthority{authority: fake, authorize: func(*http.Request) bool { return allowed }}
	req := httptest.NewRequest("POST", "http://agentd/v1/native-sessions/"+uuid.NewString()+"/attach", nil)
	if _, err := a.Authenticate(req); err == nil || fake.calls != 0 {
		t.Fatal("socket denial reached person authority")
	}
	allowed = true
	if _, err := a.Authenticate(req); err == nil || fake.calls != 0 {
		t.Fatal("missing kernel peer reached person authority")
	}
	peer, err := observeAttachProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(context.WithValue(req.Context(), attachPeerKey{}, peer))
	fake.actor.PrincipalKind = "agent"
	_, err = a.Authenticate(req)
	var refusal *nativecontract.ValidationError
	if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonWrongPrincipal {
		t.Fatal("paired agent became a person", err)
	}
	fake.actor = person
	actor, err := a.Authenticate(req)
	if err != nil || actor.PrincipalID != person.PrincipalID {
		t.Fatal("verified person was lost", err)
	}
	if _, ok := req.Context().Value(nativeLocalRequestKey{}).(nativeLocalRequest); !ok {
		t.Fatal("viewer provenance was not retained")
	}
	called := false
	err = a.WithAccess(req.Context(), actor, nativestream.LocalRequest{}, func(nativecontract.ValidationContext) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("person verification substituted for a current server grant")
	}
}

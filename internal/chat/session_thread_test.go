// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: the session-panel lookup reveals another person's conversation, or a
// superseded binding keeps a stale live stream reachable from the old session.
func TestSessionThreadLookupFollowsOnlyTheCallersCurrentBinding(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "session-lookup"))
	first, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
	unbound, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
	path := func(session string) string { return "/api/chat-sessions/" + session + "/thread" }

	expect(t, f.call(f.alice, "GET", path(first), nil, ""), 404)
	f.bind(t, f.alice, thread, first, "0")
	got := decode[Thread](t, f.call(f.alice, "GET", path(first), nil, ""))
	if got.ID != thread.ID || got.BindingEpoch != "1" || got.Contract != "chat-v1" {
		t.Fatalf("lookup returned %+v, want thread %s at epoch 1", got, thread.ID)
	}
	for _, p := range []tenant.Principal{f.bob, f.admin, f.foreign, f.agent} {
		w := f.call(p, "GET", path(first), nil, "")
		expect(t, w, 404)
		if strings.Contains(w.Body.String(), thread.ID) {
			t.Fatal("lookup leaked the conversation id to a non-participant")
		}
	}
	expect(t, f.call(f.alice, "GET", path(unbound), nil, ""), 404)
	expect(t, f.call(f.alice, "GET", path("not-a-uuid"), nil, ""), 404)

	second, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
	f.bind(t, f.alice, thread, second, "1")
	expect(t, f.call(f.alice, "GET", path(first), nil, ""), 404)
	if moved := decode[Thread](t, f.call(f.alice, "GET", path(second), nil, "")); moved.ID != thread.ID || moved.BindingEpoch != "2" {
		t.Fatalf("handover lookup returned %+v", moved)
	}

	mux := http.NewServeMux()
	New(nil).Mount(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path(second), nil).WithContext(tenant.WithPrincipal(t.Context(), f.alice)))
	expect(t, w, 404)
}

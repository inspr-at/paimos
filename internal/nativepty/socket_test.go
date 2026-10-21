// SPDX-License-Identifier: AGPL-3.0-only

package nativepty

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/nativeattach"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

// Risk: a PTY subscription loses its independent person proof or current
// generation even though the preceding attach succeeded. Check the actual
// request and a validated snapshot across the socket, without a real process.
func TestNativePTYSocketCarriesPersonAndGeneration(t *testing.T) {
	_, fixture := clientFixture(t, nil)
	view := fixture.view
	view.Mode, view.State, view.Generation = "terminal", "taken", 7
	b := *view.CurrentBinding
	viewer := uuid.NewString()
	const cookie = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	proof, err := nativeattach.NewPersonProof(client.NewSession("https://fixture.invalid", cookie))
	if err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("", "nt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	short, err = filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(short, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(short, "agentd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(socket+".token", []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := nativestream.Encode(v, "NativeSessionView", view)
	if err != nil {
		t.Fatal(err)
	}
	event := nativecontract.StreamEvent{Binding: b, Generation: view.Generation, Sequence: "0", Seq: "0", Source: "native", ObservedAt: fixture.clock.Now(), Kind: "snapshot", Payload: payload, DroppedFrames: "0"}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
		person, err := r.Cookie("aeon_session")
		q := r.URL.Query()
		if err != nil || person.Value != cookie || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) || r.Header.Get(nativecontract.ViewerHeader) != viewer || r.Header.Get(nativecontract.ControlHeader) != "" || r.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol || r.URL.Path != localPath(b, "stream") || q.Get("generation") != strconv.FormatInt(view.Generation, 10) || q.Get("expected_runtime_epoch") != b.RuntimeEpoch || q.Get("expected_harness_session_id") != b.HarnessSessionID || q.Get("expected_binding_revision") != strconv.FormatInt(b.BindingRevision, 10) || q.Get("view") != "terminal" {
			t.Error("PTY subscription lost person/viewer/generation separation")
			http.Error(w, "invalid subscription", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "id: 7:0\nevent: snapshot\ndata: %s\n\n", data)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	wire, err := NewSocketWire(socket, proof)
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	observed := errors.New("snapshot observed")
	err = wire.Watch(t.Context(), view, viewer, func(got nativecontract.StreamEvent) error {
		if got.Kind != "snapshot" || got.Generation != view.Generation || !got.Binding.Equal(b) {
			t.Fatal("foreign snapshot was delivered")
		}
		return observed
	})
	if !errors.Is(err, observed) {
		t.Fatalf("subscription did not deliver its exact snapshot: %v", err)
	}
}

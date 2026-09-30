//go:build darwin || linux

// SPDX-License-Identifier: AGPL-3.0-only
package agentdwire

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

func TestAttachConflictDiagnosticIsSurfacedAndBounded(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "aeon-attach-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "agentd.sock")
	token := socket + ".token"
	if err = os.WriteFile(token, []byte("12345678901234567890123456789012"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var body atomic.Value
	body.Store(`{"code":"harness_identity_mismatch","hint":"Repair the local harness identity."}`)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer 12345678901234567890123456789012" {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(body.Load().(string)))
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client := Client{Socket: socket, TokenFile: token}
	_, err = client.Attach(t.Context(), agentd.AttachLocalRequest{Operation: "preview"})
	var detail *agentd.AttachLocalError
	if !errors.As(err, &detail) || detail.Code != "harness_identity_mismatch" || !strings.Contains(err.Error(), "Repair the local harness identity") {
		t.Fatal("409 diagnostic hidden", err)
	}
	body.Store(`{"code":"harness_identity_unavailable","hint":"Retry attach after verification becomes available."}`)
	_, err = client.Attach(t.Context(), agentd.AttachLocalRequest{Operation: "preview"})
	if !errors.As(err, &detail) || detail.Code != "harness_identity_unavailable" || !strings.Contains(err.Error(), "Retry attach") {
		t.Fatal("retry diagnostic hidden", err)
	}
	body.Store(`{"code":"harness_identity_unsupported","hint":"Cursor attach is unavailable on macOS until a signed cursor-agent CLI is available. Cursor.app cannot identify a Cursor harness."}`)
	_, err = client.Attach(t.Context(), agentd.AttachLocalRequest{Operation: "preview"})
	if !errors.As(err, &detail) || detail.Code != "harness_identity_unsupported" || detail.Hint != "Cursor attach is unavailable on macOS until a signed cursor-agent CLI is available. Cursor.app cannot identify a Cursor harness." {
		t.Fatal("unsupported harness diagnostic hidden", err)
	}
	body.Store(`{"code":"harness_identity_unsupported","hint":"This Claude process was started with a BUN_* assignment other than BUN_INSTALL. The signed runtime can use those variables to run other JavaScript. Unset them, then attach again."}`)
	_, err = client.Attach(t.Context(), agentd.AttachLocalRequest{Operation: "preview"})
	if !errors.As(err, &detail) || detail.Code != "harness_identity_unsupported" || detail.Hint != "This Claude process was started with a BUN_* assignment other than BUN_INSTALL. The signed runtime can use those variables to run other JavaScript. Unset them, then attach again." {
		t.Fatal("BUN_OPTIONS diagnostic hidden", err)
	}
	body.Store(`{"code":"harness_identity_unsupported","hint":"This Claude process cannot be attached because macOS hid its environment, and the signed executable can run JavaScript from a BUN_* variable."}`)
	_, err = client.Attach(t.Context(), agentd.AttachLocalRequest{Operation: "preview"})
	if !errors.As(err, &detail) || detail.Code != "harness_identity_unsupported" || detail.Hint != "This Claude process cannot be attached because macOS hid its environment, and the signed executable can run JavaScript from a BUN_* variable." {
		t.Fatal("hidden Claude environment diagnostic hidden", err)
	}
	for _, bad := range []string{strings.Repeat("x", 1025), `{"code":"harness_identity_mismatch","hint":"\u001b[31munsafe"}`, `{"code":"unknown","hint":"unreviewed"}`} {
		body.Store(bad)
		if _, err = client.Attach(t.Context(), agentd.AttachLocalRequest{}); err == nil || err.Error() != "local lifecycle request rejected" {
			t.Fatal("unsafe diagnostic surfaced", err)
		}
	}
	body.Store("watch requires --transcript PATH; choose --status-only for no conversation text\n")
	if _, err = client.Attach(t.Context(), agentd.AttachLocalRequest{}); err == nil || !strings.Contains(err.Error(), "watch requires") {
		t.Fatal("legacy local diagnostic hidden", err)
	}
	if _, err = client.Lifecycle(t.Context(), ""); err == nil || err.Error() != "local lifecycle request rejected" {
		t.Fatal("lifecycle contract changed", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/hooknote"
)

type memNotes struct{ offers int }

func (m *memNotes) Offer(context.Context, hooknote.Binding) (hooknote.Note, string, error) {
	m.offers++
	return hooknote.Note{}, "", hooknote.ErrEmpty
}

func (m *memNotes) Settle(context.Context, string, string, hooknote.Epoch) error { return nil }

func TestHookPeerRouteStaysOff(t *testing.T) {
	if hooknote.Enabled() {
		t.Fatal("release switch defaulted on")
	}
	socket, _, token := hookPeerServer(t)
	status, _ := postHook(t, socket, "Authorization: Bearer "+string(token), `{"op":"offer","event":"PostToolUse"}`)
	if status != 404 {
		t.Fatalf("disabled hook route %d", status)
	}
}

func TestHookPeerIgnoresSocketToken(t *testing.T) {
	hooknote.EnableForTest(t.Cleanup)
	socket, local, token := hookPeerServer(t)
	notes := &memNotes{}
	local.SetHookPeer(notes, hooknote.NewRegistry())
	status, body := postHook(t, socket, "Authorization: Bearer "+string(token), `{"op":"offer","event":"PostToolUse"}`)
	if status == 200 || notes.offers != 0 || body == "" {
		t.Fatalf("token authorized inbox-hook status %d offers %d", status, notes.offers)
	}
	if postStatus(t, socket, string(token)) != 200 {
		t.Fatal("status token rejected")
	}
	if postStatus(t, socket, "not-the-token") == 200 {
		t.Fatal("status accepted a bad token")
	}
}

func hookPeerServer(t *testing.T) (string, *LocalServer, []byte) {
	t.Helper()
	s, _, _ := testSupervisor(t)
	dir, err := os.MkdirTemp("/tmp", "aeon-hook-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agentd.sock")
	local, err := ServeLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	token, err := os.ReadFile(socket + ".token")
	if err != nil {
		t.Fatal(err)
	}
	return socket, local, token
}

func postHook(t *testing.T, socket, header, body string) (int, string) {
	t.Helper()
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw := "POST /v1/inbox-hook HTTP/1.1\r\nHost: agentd\r\nContent-Type: application/json\r\nContent-Length: " + itoaHook(len(body)) + "\r\n"
	if header != "" {
		raw += header + "\r\n"
	}
	raw += "Connection: close\r\n\r\n" + body
	if _, err = io.WriteString(c, raw); err != nil {
		t.Fatal(err)
	}
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return httpStatus(string(buf)), string(buf)
}

func postStatus(t *testing.T, socket, token string) int {
	t.Helper()
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := "GET /v1/status HTTP/1.1\r\nHost: agentd\r\nAuthorization: Bearer " + token + "\r\nConnection: close\r\n\r\n"
	if _, err = io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	buf, _ := io.ReadAll(c)
	return httpStatus(string(buf))
}

func httpStatus(text string) int {
	i := indexByte(text, ' ')
	if i < 0 {
		return 0
	}
	n := 0
	for _, ch := range text[i+1:] {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func itoaHook(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func (*memNotes) Validate(context.Context, string, hooknote.Binding) error { return nil }

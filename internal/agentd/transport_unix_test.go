// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSocketAuthAndBound(t *testing.T) {
	s, _, _ := testSupervisor(t)
	short, err := os.MkdirTemp("/tmp", "aeon-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	short, err = filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(short, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(short, "agentd.sock")
	local, err := ServeLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	defer s.Close(context.Background())
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", info, err)
	}
	token, err := os.ReadFile(socket + ".token")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	request, _ := http.NewRequest("GET", "http://agentd/v1/status", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("unauthenticated status %d", response.StatusCode)
	}
	request, _ = http.NewRequest("GET", "http://agentd/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+string(token))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("authenticated status %d", response.StatusCode)
	}
	// Capacity is opt-in, preserving strict lifecycle decoders during upgrades.
	for _, query := range []string{"", "?include_capacity=1", "?include_capacity=1&account_id=missing", "?include_readiness=1"} {
		request, _ = http.NewRequest("GET", "http://agentd/v1/lifecycle"+query, nil)
		request.Header.Set("Authorization", "Bearer "+string(token))
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		_, readiness := body["account_statuses"]
		if readiness != strings.Contains(query, "include_readiness=1") {
			t.Fatal("readiness changed legacy lifecycle contract")
		}
		_, present := body["capacity_accounts"]
		if present != strings.Contains(query, "include_capacity=1") {
			t.Fatal("capacity changed the ordinary lifecycle contract")
		}
		if strings.Contains(query, "missing") && string(body["capacity_accounts"]) != "[]" {
			t.Fatal("empty selection is not an empty inventory")
		}
	}
	request, _ = http.NewRequest("POST", "http://agentd/v1/runs/run/control", strings.NewReader(strings.Repeat("x", 71<<10)))
	request.Header.Set("Authorization", "Bearer "+string(token))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("oversized control status %d", response.StatusCode)
	}
}

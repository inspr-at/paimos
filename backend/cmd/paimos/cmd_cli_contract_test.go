package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLICommandGroupsRejectUnknownAndMissingSubcommands(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "paimos")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOCACHE="+t.TempDir())
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	cases := [][]string{
		{"worker", "strat"},
		{"project", "get", "PAI"},
		{"auth", "status"},
		{"worker"},
		{"project"},
		{"auth"},
	}
	for _, args := range cases {
		command := exec.Command(bin, args...)
		output, err := command.CombinedOutput()
		if err == nil {
			t.Errorf("%s exited 0; output=%q", strings.Join(args, " "), output)
			continue
		}
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() == 0 {
			t.Errorf("%s returned non-usage error %v; output=%q", strings.Join(args, " "), err, output)
		}
	}

	for _, args := range [][]string{{"worker", "--help"}, {"worker", "start", "--help"}, {"project", "--help"}, {"auth", "--help"}} {
		command := exec.Command(bin, args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("explicit help %s failed: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	command := exec.Command(bin, "curl", "/api/secret")
	command.Env = append(os.Environ(), envURL+"="+server.URL, envAPIKey+"=test_key")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("human curl HTTP failure exited 0")
	}
	if !strings.Contains(string(output), "Error: API error 403") || strings.Count(string(output), "forbidden") != 1 {
		t.Fatalf("human curl failure output=%q, want one reported error", output)
	}
}

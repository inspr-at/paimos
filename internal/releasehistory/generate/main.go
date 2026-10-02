// SPDX-License-Identifier: AGPL-3.0-only

// Command generate writes the release history manifest (inspr.release-history.v1)
// that the server embeds. The release workflow runs it after a full-depth checkout
// (tags included) and before the image build:
//
//	go run ./internal/releasehistory/generate -repo . -repository inspr-at/paimos
//
// inspr-at/paimos is the origin name (GITHUB_REPOSITORY). inspr-at/aeon is the
// same product; product notes match either.
//
// With GITHUB_TOKEN in the environment it adds publication times, image digests
// and CI runs; -offline skips GitHub. The token is never printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

func main() {
	repo := flag.String("repo", ".", "git working tree with tags")
	out := flag.String("out", "internal/releasehistory/data/history.json", "manifest to write")
	repository := flag.String("repository", os.Getenv("GITHUB_REPOSITORY"), "GitHub owner/name for links and evidence")
	candidate := flag.String("candidate", "", "embed this committed version before its release tag exists")
	offline := flag.Bool("offline", false, "do not read GitHub")
	flag.Parse()
	opts := releasehistory.Options{Repo: *repo, Repository: *repository, Candidate: *candidate}
	if *candidate != "" {
		opts.CandidateCI = candidateRun(*repository, os.Getenv)
	}
	if !*offline && *repository != "" {
		opts.GitHub = &releasehistory.GitHub{Token: os.Getenv("GITHUB_TOKEN")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h, err := releasehistory.Build(ctx, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release history:", err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "release history:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "release history:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "release history:", err)
		os.Exit(1)
	}
	published := 0
	for _, r := range h.Releases {
		if r.State == releasehistory.StatePublished {
			published++
		}
	}
	fmt.Printf("release history: %d releases (%d published) from %s -> %s\n", len(h.Releases), published, h.Source, *out)
}

// The building workflow is still running. Its conclusion cannot be known here.
func candidateRun(repository string, getenv func(string) string) *releasehistory.Run {
	id := getenv("GITHUB_RUN_ID")
	if !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(id) || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) {
		return nil
	}
	server := getenv("GITHUB_SERVER_URL")
	if server == "" {
		server = "https://github.com"
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil
	}
	name := getenv("GITHUB_WORKFLOW")
	if name == "" {
		name = "CI"
	}
	return &releasehistory.Run{Name: name, URL: strings.TrimRight(server, "/") + "/" + repository + "/actions/runs/" + id, Status: "in_progress"}
}

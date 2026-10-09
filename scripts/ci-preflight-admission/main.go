// SPDX-License-Identifier: AGPL-3.0-only
// The controller installs this checker outside builder-controlled workspaces.
// Existing gh authentication supplies read-only repository observations; no
// status write, push, PR creation, merge or dispatch happens here.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.limit {
		return 0, fmt.Errorf("preflight_response_oversized")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func github(ctx context.Context, path string, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", path)
	out := boundedBuffer{limit: limit}
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("preflight_lookup_failed")
	}
	return out.data, nil
}
func check(ctx context.Context, repository, sha string, out io.Writer) bool {
	reason := "preflight_invalid"
	if reviewgate.ValidRepository(repository) && reviewgate.ValidSHA(sha) {
		var err error
		reason, err = delivery.LookupPreflight(func(path string, value any) error {
			raw, err := github(ctx, "repos/"+repository+path, 4<<20)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, value)
		}, func(id int64) ([]byte, error) {
			return github(ctx, fmt.Sprintf("repos/%s/actions/artifacts/%d/zip", repository, id), 64<<10)
		}, sha)
		if err != nil {
			reason = "preflight_lookup_failed"
		}
	}
	allowed := reason == ""
	if allowed {
		reason = "preflight_passed"
	}
	_ = json.NewEncoder(out).Encode(struct {
		Context string `json:"context"`
		SHA     string `json:"sha"`
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
	}{"paimos/admission", sha, allowed, reason})
	return allowed
}
func main() {
	flags := flag.NewFlagSet("paimos/admission check", flag.ExitOnError)
	repository := flags.String("repository", "", "bound repository owner/name")
	sha := flags.String("sha", "", "exact proposed PR head")
	if len(os.Args) < 2 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "Usage: ci-preflight-admission check --repository OWNER/NAME --sha SHA")
		os.Exit(1)
	}
	flags.Parse(os.Args[2:])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if flags.NArg() != 0 || !check(ctx, *repository, *sha, os.Stdout) {
		os.Exit(1)
	}
}

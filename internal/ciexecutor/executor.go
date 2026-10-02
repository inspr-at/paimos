// SPDX-License-Identifier: AGPL-3.0-only

// Package ciexecutor is the immutable, credential-free guest init/runner. It
// never selects obligations, signs receipts, publishes checks or chooses tools.
package ciexecutor

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inspr-at/paimos/internal/ciproof"
)

const maxSource = 128 << 20

// GuestEnvironment is generated anew for every process. Never merge the host,
// install-script environment, GITHUB_ENV, NODE_OPTIONS, BASH_ENV or shell state.
func GuestEnvironment() []string {
	return []string{
		"PATH=/opt/aeon/bin:/usr/bin:/bin", "HOME=/workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "CI=1",
		"GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOPROXY=off", "GOSUMDB=off", "GOMODCACHE=/opt/aeon/go/pkg/mod", "GOCACHE=/workspace/go-cache",
		"npm_config_offline=true", "npm_config_cache=/workspace/npm-cache", "PLAYWRIGHT_BROWSERS_PATH=/opt/aeon/browsers",
	}
}

// ExtractSource accepts only bounded regular entries. It never imports links,
// special files, archive ownership, absolute paths or a candidate .git/config.
// root is a fresh tmpfs, never a host directory or a mounted executor image.
func ExtractSource(raw []byte, root string) error {
	if len(raw) > maxSource || !filepath.IsAbs(root) {
		return fmt.Errorf("candidate source archive limit")
	}
	r := tar.NewReader(bytes.NewReader(raw))
	seen := map[string]bool{}
	total := int64(0)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid candidate archive")
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxSource || path.Clean(h.Name) != h.Name || h.Name == "." || strings.HasPrefix(h.Name, "/") || strings.HasPrefix(h.Name, "../") || strings.ContainsAny(h.Name, "\\\x00") || h.Name == ".git" || strings.HasPrefix(h.Name, ".git/") || seen[h.Name] || len(seen) >= 100000 || (h.Mode != 0644 && h.Mode != 0755) {
			return fmt.Errorf("unsupported candidate archive entry")
		}
		seen[h.Name] = true
		total += h.Size
		if total > maxSource {
			return fmt.Errorf("candidate source size limit")
		}
		name := filepath.Join(root, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return fmt.Errorf("guest source directory unavailable")
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode))
		if err != nil {
			return fmt.Errorf("guest source file unavailable")
		}
		_, copyErr := io.CopyN(f, r, h.Size)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("guest source extraction incomplete")
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("empty candidate source archive")
	}
	return nil
}

// Completion parses only the approved recipe's typed manifest. This is not a
// trust anchor for unadmitted executable bytes: malicious Go init/helper code
// can fabricate its own stdout, which is why independent whole-tree admission
// is mandatory before the external supervisor accepts even an observation.
func Completion(task ciproof.VMTask, output []byte, exitCode int) (ciproof.ExecutionManifest, error) {
	executed := []string{}
	if exitCode != 0 {
		return ciproof.ExecutionManifest{}, fmt.Errorf("guest command failed")
	}
	switch task.Reporter {
	case "command":
		executed = append(executed, task.Expected...)
	case "harness-json":
		// A browser/app VM link and approved reporter are delivered by D/H.
		// No candidate Playwright stdout/report can stand in for that integration.
		return ciproof.ExecutionManifest{}, fmt.Errorf("approved browser/application boundary not provisioned")
	case "go-json":
		d := json.NewDecoder(bytes.NewReader(output))
		passed := map[string]bool{}
		started := map[string]bool{}
		for {
			var event struct {
				Action  string
				Package string
				Test    string
				Output  string
				Elapsed float64
				Time    string
			}
			if err := d.Decode(&event); err == io.EOF {
				break
			} else if err != nil {
				return ciproof.ExecutionManifest{}, fmt.Errorf("invalid Go execution stream")
			}
			if event.Package == "" {
				return ciproof.ExecutionManifest{}, fmt.Errorf("Go result package missing")
			}
			if event.Action == "fail" || event.Action == "skip" {
				return ciproof.ExecutionManifest{}, fmt.Errorf("failed or skipped Go execution")
			}
			id := "package/" + event.Package
			if event.Test != "" {
				id = "test/" + event.Package + "/" + event.Test
			}
			switch event.Action {
			case "start", "run":
				if started[id] {
					return ciproof.ExecutionManifest{}, fmt.Errorf("duplicate Go execution start")
				}
				started[id] = true
			case "pass":
				if !started[id] || passed[id] {
					return ciproof.ExecutionManifest{}, fmt.Errorf("missing start or duplicate Go terminal result")
				}
				passed[id] = true
			case "output", "pause", "cont", "build-output":
			default:
				return ciproof.ExecutionManifest{}, fmt.Errorf("unknown Go execution event")
			}
		}
		for id := range started {
			if !passed[id] {
				return ciproof.ExecutionManifest{}, fmt.Errorf("Go execution missing terminal completion")
			}
		}
		for id := range passed {
			executed = append(executed, id)
		}
		sort.Strings(executed)
	default:
		return ciproof.ExecutionManifest{}, fmt.Errorf("unsupported approved reporter")
	}
	if len(task.Expected) == 0 || strings.Join(executed, "\x00") != strings.Join(task.Expected, "\x00") {
		return ciproof.ExecutionManifest{}, fmt.Errorf("missing or unexpected execution identities")
	}
	kind := "test"
	if task.Reporter == "command" {
		kind = "command"
	}
	return ciproof.ExecutionManifest{Kind: kind, Expected: append([]string(nil), task.Expected...), Executed: executed, Skipped: []string{}}, nil
}

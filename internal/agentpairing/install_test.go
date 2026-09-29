// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/version"
)

const installerTestVersion = "260927200000.0.0"

type installerFixture struct {
	home, targetDir, curlCalls, asset, command string
}

func newInstallerFixture(t *testing.T, corrupt bool) installerFixture {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("installer targets macOS and Linux")
	}
	oldVersion := version.Version
	version.Version = installerTestVersion
	t.Cleanup(func() { version.Version = oldVersion })

	var target InstallTarget
	for _, candidate := range installTargets() {
		if candidate.Platform == runtime.GOOS && candidate.Arch == runtime.GOARCH {
			target = candidate
			break
		}
	}
	if target.Command == "" {
		t.Skip("no installer for this architecture")
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureDir := filepath.Join(root, "release")
	toolsDir := filepath.Join(root, "tools")
	for _, dir := range []string{fixtureDir, toolsDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	asset := "paimos-agentd-" + target.Platform + "-" + target.Arch
	data := []byte("synthetic release executable bytes\n")
	if err := os.WriteFile(filepath.Join(fixtureDir, asset), data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if corrupt {
		hash = sha256.Sum256([]byte("other bytes"))
	}
	manifest := fmt.Sprintf("%064x  unrelated-asset\n%x  %s\n", sha256.Sum256(nil), hash, asset)
	if err := os.WriteFile(filepath.Join(fixtureDir, "SHA256SUMS"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mkdir", "stat", "id", "awk", "install", "sha256sum", "shasum"} {
		path, err := exec.LookPath(name)
		if name == "stat" && runtime.GOOS == "darwin" {
			path, err = "/usr/bin/stat", nil
		}
		if err != nil {
			if name == "sha256sum" && runtime.GOOS == "darwin" || name == "shasum" && runtime.GOOS == "linux" {
				continue
			}
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(toolsDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	cp, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	curl := fmt.Sprintf(`#!/bin/sh
out=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) out=$2; shift 2 ;;
    *) url=$1; shift ;;
  esac
done
printf '%%s\n' "$url" >> "$CURL_CALLS"
case "$url" in
  https://github.com/inspr-at/paimos/releases/download/v%s/%s|https://github.com/inspr-at/paimos/releases/download/v%s/SHA256SUMS) ;;
  *) exit 2 ;;
esac
%s "$FIXTURE_DIR/${url##*/}" "$out"
`, installerTestVersion, asset, installerTestVersion, shellQuote(cp))
	if err := os.WriteFile(filepath.Join(toolsDir, "curl"), []byte(curl), 0700); err != nil {
		t.Fatal(err)
	}
	return installerFixture{
		home: home, asset: asset, command: target.Command,
		targetDir: filepath.Join(home, ".local", "lib", "aeon", installerTestVersion, target.Platform+"-"+target.Arch),
		curlCalls: filepath.Join(root, "curl-calls"),
	}
}

func (f installerFixture) run(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", f.command)
	cmd.Env = []string{
		"HOME=" + f.home,
		"PATH=" + filepath.Join(filepath.Dir(f.curlCalls), "tools"),
		"FIXTURE_DIR=" + filepath.Join(filepath.Dir(f.curlCalls), "release"),
		"CURL_CALLS=" + f.curlCalls,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("installer refused: %s", strings.TrimSpace(string(out)))
	}
	return err
}

func assertNoDownload(t *testing.T, f installerFixture) {
	t.Helper()
	if _, err := os.Stat(f.curlCalls); !os.IsNotExist(err) {
		t.Fatalf("download attempted before path rejection: %v", err)
	}
}

func TestGeneratedInstallerPrivateSuccess(t *testing.T) {
	f := newInstallerFixture(t, false)
	if err := f.run(t); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(f.targetDir, "paimos-agentd")
	got, err := os.ReadFile(installed)
	if err != nil || string(got) != "synthetic release executable bytes\n" {
		t.Fatalf("installed bytes: %q, %v", got, err)
	}
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("installed mode: %v, %v", info, err)
	}
	calls, err := os.ReadFile(f.curlCalls)
	if err != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 2 {
		t.Fatalf("expected exactly two fixture downloads: %q, %v", calls, err)
	}
}

func TestGeneratedInstallerPreservesExistingAeonAndNix(t *testing.T) {
	for _, tool := range []string{"aeon-agentd", "paimos-agentd", "nix"} {
		t.Run(tool, func(t *testing.T) {
			f := newInstallerFixture(t, false)
			path := filepath.Join(filepath.Dir(f.curlCalls), "tools", tool)
			// A guard fixture must never be executed, installed over or removed.
			contents := []byte("#!/bin/sh\nexit 97\n")
			if err := os.WriteFile(path, contents, 0700); err != nil {
				t.Fatal(err)
			}
			if err := f.run(t); err == nil {
				t.Fatal("installed over an existing tool")
			}
			assertNoDownload(t, f)
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(contents) {
				t.Fatal("changed existing tool")
			}
			if _, err := os.Stat(f.targetDir); !os.IsNotExist(err) {
				t.Fatal("created installer destination")
			}
		})
	}
}

func TestGeneratedInstallerRejectsUnsafeAncestors(t *testing.T) {
	for _, ancestor := range []string{"home", ".local", ".local/lib", ".local/lib/aeon", ".local/lib/aeon/" + installerTestVersion} {
		for _, condition := range []string{"symlink", "writable", "foreign"} {
			t.Run(ancestor+"/"+condition, func(t *testing.T) {
				f := newInstallerFixture(t, false)
				path := filepath.Join(f.home, ancestor)
				if ancestor == "home" {
					path = f.home
				}
				if condition == "symlink" {
					// A link at HOME needs to replace the fixture's empty home directory.
					if ancestor == "home" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					outside := filepath.Join(filepath.Dir(f.home), "outside")
					if err := os.Mkdir(outside, 0700); err != nil {
						t.Fatal(err)
					}
					if ancestor != "home" {
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(outside, path); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.MkdirAll(path, 0700); err != nil && ancestor != "home" {
						t.Fatal(err)
					}
					if condition == "writable" {
						if err := os.Chmod(path, 0770); err != nil {
							t.Fatal(err)
						}
					} else {
						// A scoped stat wrapper emulates an ancestor owned by a different UID.
						statPath, err := exec.LookPath("stat")
						if runtime.GOOS == "darwin" {
							statPath, err = "/usr/bin/stat", nil
						}
						if err != nil {
							t.Fatal(err)
						}
						wrapper := fmt.Sprintf("#!/bin/sh\ncase \"$1:$2:$3\" in '-c:%%u:%s'|'-f:%%u:%s') echo 999999; exit;; esac\nexec %s \"$@\"\n", path, path, shellQuote(statPath))
						tool := filepath.Join(filepath.Dir(f.curlCalls), "tools", "stat")
						if err := os.Remove(tool); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(tool, []byte(wrapper), 0700); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := f.run(t); err == nil {
					t.Fatal("unsafe ancestor accepted")
				}
				assertNoDownload(t, f)
				if _, err := os.Stat(f.targetDir); !os.IsNotExist(err) {
					t.Fatalf("destination created: %v", err)
				}
			})
		}
	}
}

func TestGeneratedInstallerRefusesExistingDestination(t *testing.T) {
	f := newInstallerFixture(t, false)
	if err := os.MkdirAll(f.targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(f.targetDir, "paimos-agentd")
	if err := os.WriteFile(sentinel, []byte("unrelated bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t); err == nil {
		t.Fatal("existing destination accepted")
	}
	assertNoDownload(t, f)
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "unrelated bytes" {
		t.Fatalf("existing bytes changed: %q, %v", got, err)
	}
}

func TestGeneratedInstallerRejectsCorruptHash(t *testing.T) {
	f := newInstallerFixture(t, true)
	if err := f.run(t); err == nil {
		t.Fatal("corrupt release accepted")
	}
	if _, err := os.Stat(filepath.Join(f.targetDir, "paimos-agentd")); !os.IsNotExist(err) {
		t.Fatalf("executable created after hash failure: %v", err)
	}
	info, err := os.Stat(filepath.Join(f.targetDir, f.asset))
	if err != nil || info.Mode().Perm()&0111 != 0 {
		t.Fatalf("downloaded asset became executable: %v, %v", info, err)
	}
}

func TestGeneratedInstallerRequiresExactChecksumFilename(t *testing.T) {
	f := newInstallerFixture(t, false)
	manifestPath := filepath.Join(filepath.Dir(f.curlCalls), "release", "SHA256SUMS")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongName := strings.ReplaceAll(string(manifest), f.asset, f.asset+"-other")
	if err := os.WriteFile(manifestPath, []byte(wrongName), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t); err == nil {
		t.Fatal("manifest entry for another filename accepted")
	}
	if _, err := os.Stat(filepath.Join(f.targetDir, "paimos-agentd")); !os.IsNotExist(err) {
		t.Fatalf("executable created without an exact checksum entry: %v", err)
	}
}

func TestGeneratedInstallerRejectsDisguisedHomeLink(t *testing.T) {
	f := newInstallerFixture(t, false)
	outside := filepath.Join(filepath.Dir(f.home), "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.home); err != nil {
		t.Fatal(err)
	}
	f.home += "/"
	if err := f.run(t); err == nil {
		t.Fatal("HOME link with a trailing slash accepted")
	}
	assertNoDownload(t, f)
	if _, err := os.Stat(filepath.Join(outside, ".local")); !os.IsNotExist(err) {
		t.Fatalf("wrote through HOME link: %v", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"bytes"
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

const (
	installerTestVersion = "260927200000.0.0"
	installerTestOrigin  = "https://pairing.test"
)

func namedPairLine(origin string) string {
	return "aeon-agentd pair --url " + shellQuote(origin) + "\n"
}

func absolutePairLine(home, origin string) string {
	return shellQuote(filepath.Join(home, ".local", "bin", "aeon-agentd")) + " pair --url " + shellQuote(origin) + "\n"
}

func pathHint(home string) string {
	return "Add " + shellQuote(filepath.Join(home, ".local", "bin")) + " to PATH to run aeon-agentd by name.\n"
}

type installerFixture struct {
	home, targetDir, curlCalls, asset, command string
}

func newInstallerFixture(t *testing.T, corrupt bool) installerFixture {
	t.Helper()
	return newInstallerFixtureOrigin(t, corrupt, installerTestOrigin)
}

func newInstallerFixtureOrigin(t *testing.T, corrupt bool, origin string) installerFixture {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("installer targets macOS and Linux")
	}
	oldVersion := version.Version
	version.Version = installerTestVersion
	t.Cleanup(func() { version.Version = oldVersion })

	var target InstallTarget
	for _, candidate := range installTargets(origin) {
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
	for _, name := range []string{"mkdir", "stat", "id", "awk", "install", "sha256sum", "shasum", "ln", "readlink", "cat", "rm"} {
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

func (f installerFixture) toolsPath() string {
	return filepath.Join(filepath.Dir(f.curlCalls), "tools")
}

func (f installerFixture) execute(t *testing.T, path string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", f.command)
	cmd.Env = []string{
		"HOME=" + f.home,
		"PATH=" + path,
		"FIXTURE_DIR=" + filepath.Join(filepath.Dir(f.curlCalls), "release"),
		"CURL_CALLS=" + f.curlCalls,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		t.Logf("installer refused: %s", strings.TrimSpace(stdout.String()+"\n"+stderr.String()))
	}
	return stdout.String(), stderr.String(), err
}

func (f installerFixture) run(t *testing.T) (string, error) {
	t.Helper()
	stdout, stderr, err := f.execute(t, f.toolsPath())
	return stdout + stderr, err
}

func assertNoDownload(t *testing.T, f installerFixture) {
	t.Helper()
	if _, err := os.Stat(f.curlCalls); !os.IsNotExist(err) {
		t.Fatalf("download attempted before path rejection: %v", err)
	}
}

func TestGeneratedInstallerPrivateSuccess(t *testing.T) {
	f := newInstallerFixture(t, false)
	stdout, stderr, err := f.execute(t, f.toolsPath())
	if err != nil {
		t.Fatal(err)
	}
	if stdout != absolutePairLine(f.home, installerTestOrigin) || stderr != pathHint(f.home) {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
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
	link := filepath.Join(f.home, ".local", "bin", "aeon-agentd")
	target, err := os.Readlink(link)
	if err != nil || target != installed {
		t.Fatalf("aeon-agentd link = %q, %v", target, err)
	}
	linked, err := os.ReadFile(link)
	if err != nil || string(linked) != "synthetic release executable bytes\n" {
		t.Fatalf("linked bytes: %q, %v", linked, err)
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
			if _, err := f.run(t); err == nil {
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
	for _, ancestor := range []string{"home", ".local", ".local/bin", ".local/lib", ".local/lib/aeon", ".local/lib/aeon/" + installerTestVersion} {
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
				if _, err := f.run(t); err == nil {
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
	if _, err := f.run(t); err == nil {
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
	if _, err := f.run(t); err == nil {
		t.Fatal("corrupt release accepted")
	}
	if _, err := os.Stat(filepath.Join(f.targetDir, "paimos-agentd")); !os.IsNotExist(err) {
		t.Fatalf("executable created after hash failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".local", "bin", "aeon-agentd")); !os.IsNotExist(err) {
		t.Fatalf("aeon-agentd linked after hash failure: %v", err)
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
	if _, err := f.run(t); err == nil {
		t.Fatal("manifest entry for another filename accepted")
	}
	if _, err := os.Stat(filepath.Join(f.targetDir, "paimos-agentd")); !os.IsNotExist(err) {
		t.Fatalf("executable created without an exact checksum entry: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".local", "bin", "aeon-agentd")); !os.IsNotExist(err) {
		t.Fatalf("aeon-agentd linked without an exact checksum entry: %v", err)
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
	if _, err := f.run(t); err == nil {
		t.Fatal("HOME link with a trailing slash accepted")
	}
	assertNoDownload(t, f)
	if _, err := os.Stat(filepath.Join(outside, ".local")); !os.IsNotExist(err) {
		t.Fatalf("wrote through HOME link: %v", err)
	}
}

func TestGeneratedInstallerRetargetsOwnedLink(t *testing.T) {
	f := newInstallerFixture(t, false)
	binDir := filepath.Join(f.home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "aeon-agentd")
	previous := filepath.Join(f.home, ".local", "lib", "aeon", "previous", "paimos-agentd")
	if err := os.Symlink(previous, link); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := f.execute(t, f.toolsPath())
	if err != nil {
		t.Fatal(err)
	}
	if stdout != absolutePairLine(f.home, installerTestOrigin) || stderr != pathHint(f.home) {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
	got, err := os.Readlink(link)
	if err != nil || got != filepath.Join(f.targetDir, "paimos-agentd") {
		t.Fatalf("retargeted link = %q, %v", got, err)
	}
}

func TestGeneratedInstallerRefusesForeignBin(t *testing.T) {
	for _, kind := range []string{"file", "outside", "dotdot"} {
		t.Run(kind, func(t *testing.T) {
			f := newInstallerFixture(t, false)
			binDir := filepath.Join(f.home, ".local", "bin")
			if err := os.MkdirAll(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(binDir, "aeon-agentd")
			switch kind {
			case "file":
				if err := os.WriteFile(link, []byte("keep"), 0700); err != nil {
					t.Fatal(err)
				}
			case "outside":
				if err := os.Symlink(filepath.Join(filepath.Dir(f.home), "outside"), link); err != nil {
					t.Fatal(err)
				}
			case "dotdot":
				if err := os.Symlink(f.home+"/.local/lib/aeon/../../outside", link); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.run(t); err == nil {
				t.Fatal("foreign aeon-agentd accepted")
			}
			assertNoDownload(t, f)
			if kind == "file" {
				got, err := os.ReadFile(link)
				if err != nil || string(got) != "keep" {
					t.Fatalf("existing file changed: %q, %v", got, err)
				}
			}
		})
	}
}

func TestPairNextLineRejectsShell(t *testing.T) {
	line, ok := pairNextLine(installerTestOrigin)
	if !ok || line != strings.TrimSuffix(namedPairLine(installerTestOrigin), "\n") {
		t.Fatalf("pair line %q %v", line, ok)
	}
	line, ok = pairNextLine("http://[::1]:8080")
	if !ok || line != "aeon-agentd pair --url 'http://[::1]:8080'" {
		t.Fatalf("ipv6 pair line %q %v", line, ok)
	}
	for _, good := range []string{"http://[::1]", "https://pairing.test:443", "http://127.0.0.1:8080", "http://[2001:db8::1]:8443"} {
		if _, ok := pairNextLine(good); !ok {
			t.Fatalf("rejected %q", good)
		}
	}
	for _, bad := range []string{"", "https://pairing.test/path", "https://pairing.test/$(id)", "http://user@pairing.test", "https://pairing.test';touch /tmp/x;'", "aeon-agentd pair --url https://pairing.test", "http://[::1]:8080/$(id)", "http://[::1]:99999", "http://::1", "http://[gggg::1]:8080", "HTTP://[::1]:8080", "https://pairing.test/", "http://[::1]:8080?x=1"} {
		if _, ok := pairNextLine(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	old := version.Version
	version.Version = installerTestVersion
	t.Cleanup(func() { version.Version = old })
	for _, target := range installTargets("https://pairing.test/$(id)") {
		if strings.Contains(target.Command, "curl") || strings.Contains(target.Command, "$(id)") || strings.Contains(target.Command, "pair --url") {
			t.Fatalf("unsafe command: %s", target.Command)
		}
	}
	for _, target := range installTargets("http://[::1]:8080/$(id)") {
		if strings.Contains(target.Command, "curl") || strings.Contains(target.Command, "$(id)") || strings.Contains(target.Command, "[::1]") {
			t.Fatalf("unsafe ipv6 command: %s", target.Command)
		}
	}
}

func assertPairLineRuns(t *testing.T, f installerFixture, line, path, origin string) {
	t.Helper()
	installed := filepath.Join(f.targetDir, "paimos-agentd")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$AEON_PAIR_ARGV\"\n"
	if err := os.WriteFile(installed, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ":"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(dir, "argv")
	cmd := exec.Command("sh", "-c", strings.TrimRight(line, "\n"))
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + f.home, "AEON_PAIR_ARGV=" + argv}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pair line: %v %s", err, out)
	}
	got, err := os.ReadFile(argv)
	if err != nil || string(got) != "pair --url "+origin+"\n" {
		t.Fatalf("argv %q %v", got, err)
	}
}

func TestGeneratedInstallerPairLineFollowsPath(t *testing.T) {
	t.Run("on-path", func(t *testing.T) {
		f := newInstallerFixture(t, false)
		path := filepath.Join(f.home, ".local", "bin") + ":" + f.toolsPath()
		stdout, stderr, err := f.execute(t, path)
		if err != nil {
			t.Fatal(err)
		}
		if stdout != namedPairLine(installerTestOrigin) || stderr != "" {
			t.Fatalf("stdout %q stderr %q", stdout, stderr)
		}
		assertPairLineRuns(t, f, stdout, filepath.Join(f.home, ".local", "bin")+":/usr/bin:/bin", installerTestOrigin)
	})
	t.Run("off-path", func(t *testing.T) {
		f := newInstallerFixture(t, false)
		stdout, stderr, err := f.execute(t, f.toolsPath())
		if err != nil {
			t.Fatal(err)
		}
		if stdout != absolutePairLine(f.home, installerTestOrigin) || stderr != pathHint(f.home) {
			t.Fatalf("stdout %q stderr %q", stdout, stderr)
		}
		assertPairLineRuns(t, f, stdout, "/usr/bin:/bin", installerTestOrigin)
	})
}

func TestGeneratedInstallerIPv6Origin(t *testing.T) {
	const origin = "http://[::1]:8080"
	f := newInstallerFixtureOrigin(t, false, origin)
	if !strings.Contains(f.command, "curl") || !strings.Contains(f.command, shellQuote(origin)) || strings.Contains(f.command, "Refusing to install without a usable instance URL") {
		t.Fatal("IPv6 origin did not render an installer")
	}
	stdout, stderr, err := f.execute(t, f.toolsPath())
	if err != nil {
		t.Fatal(err)
	}
	if stdout != absolutePairLine(f.home, origin) || stderr != pathHint(f.home) {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
	calls, err := os.ReadFile(f.curlCalls)
	if err != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 2 {
		t.Fatalf("expected exactly two fixture downloads: %q, %v", calls, err)
	}
	assertPairLineRuns(t, f, stdout, "/usr/bin:/bin", origin)
}

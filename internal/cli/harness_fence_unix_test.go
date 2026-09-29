//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every fixture here is synthetic. No real harness home is read.
const fenceSentinel = `{"type":"custom-title","customTitle":"SYNTHETIC_SENTINEL"}` + "\n"

func fenceWrite(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fenceHome returns a symlink-free temp root so a failure is never caused by
// the platform's /var -> /private/var link.
func fenceHome(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// fenceKinds is each allowed vendor file relative to its harness root.
var fenceKinds = []struct {
	name string
	kind harnessFileKind
	rel  string
}{
	{"claude", harnessClaudeTranscript, "claude/projects/-work-slug/11111111-1111-4111-8111-111111111111.jsonl"},
	{"codex", harnessCodexRollout, "codex/sessions/2026/09/29/rollout-2026-09-29T00-00-00-11111111-1111-4111-8111-111111111111.jsonl"},
	{"codex-index", harnessCodexIndex, "codex/session_index.jsonl"},
	{"grok", harnessGrokUsage, "grok/sessions/%2Fwork/abcdefgh/usage.json"},
	{"cursor", harnessCursorUsage, "state/cursor.jsonl"},
	{"agent-status", harnessAgentStatus, "work/.agent-status.json"},
}

func TestHarnessFenceAdversarial(t *testing.T) {
	credentialNames := []string{
		"auth.json", "cli-config.json", ".credentials.json", ".credentials", "credentials.json",
		"cookies", "Cookies", "cookies.db", "login.keychain-db", "id_ed25519", "api.key", ".env", ".env.local",
	}
	for _, tc := range fenceKinds {
		t.Run(tc.name, func(t *testing.T) {
			home := fenceHome(t)
			real := fenceWrite(t, filepath.Join(home, "real", tc.rel), fenceSentinel)
			denied := func(label, path string) {
				t.Helper()
				if f, err := openHarnessFile(tc.kind, path); err == nil {
					f.Close()
					t.Errorf("%s: opened %s", label, path)
				}
				if _, err := statHarnessFile(tc.kind, path); err == nil {
					t.Errorf("%s: stat accepted %s", label, path)
				}
			}
			// Control: the real vendor file opens.
			f, err := openHarnessFile(tc.kind, real)
			if err != nil {
				t.Fatalf("allowed file refused: %v", err)
			}
			f.Close()

			// Symlinked parent directory.
			if err := os.Symlink(filepath.Join(home, "real"), filepath.Join(home, "alias")); err != nil {
				t.Fatal(err)
			}
			denied("symlinked parent", filepath.Join(home, "alias", tc.rel))
			// Symlinked leaf with the allowed name.
			leaf := filepath.Join(home, "leaf", tc.rel)
			if err := os.MkdirAll(filepath.Dir(leaf), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, leaf); err != nil {
				t.Fatal(err)
			}
			denied("symlinked leaf", leaf)
			// Hard link with the allowed name.
			hard := filepath.Join(home, "hard", tc.rel)
			if err := os.MkdirAll(filepath.Dir(hard), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(real, hard); err != nil {
				t.Fatal(err)
			}
			denied("hard link", hard)
			denied("hard link original", real)

			// Credential files next to the vendor file, by absolute path.
			for _, name := range credentialNames {
				denied("credential sibling "+name, fenceWrite(t, filepath.Join(filepath.Dir(real), name), fenceSentinel))
			}
			// A credential-named ancestor, even when the file shape matches.
			for _, dir := range []string{"Keychains", "Library/Keychains", ".ssh", "Cookies", ".credentials", "secrets"} {
				denied("credential ancestor "+dir, fenceWrite(t, filepath.Join(home, dir, tc.rel), fenceSentinel))
			}
			// Absolute paths into a keychain.
			denied("keychain db", fenceWrite(t, filepath.Join(home, "Library", "Keychains", "login.keychain-db"), fenceSentinel))
			// Traversal from an allowed directory to a credential, and a
			// traversal that cleans back to an allowed shape.
			auth := fenceWrite(t, filepath.Join(home, "real", "codex", "auth.json"), fenceSentinel)
			up := strings.Repeat("../", strings.Count(tc.rel, "/")-1)
			denied("traversal to auth.json", filepath.Dir(real)+"/"+up+"auth.json")
			denied("traversal back to allowed", filepath.Dir(real)+"/../"+filepath.Base(filepath.Dir(real))+"/"+filepath.Base(real))
			if _, err := os.Stat(auth); err != nil {
				t.Fatal(err)
			}
			// Relative path judged by its absolute ancestors.
			chained := fenceWrite(t, filepath.Join(home, "Keychains", tc.rel), fenceSentinel)
			t.Chdir(filepath.Join(home, "Keychains"))
			denied("relative inside Keychains", tc.rel)
			if _, err := os.Stat(chained); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHarnessFenceKindsDoNotCross(t *testing.T) {
	home := fenceHome(t)
	for _, from := range fenceKinds {
		path := fenceWrite(t, filepath.Join(home, from.name, from.rel), fenceSentinel)
		for _, to := range fenceKinds {
			_, ok := resolveHarnessPath(to.kind, path)
			if ok != (from.kind == to.kind) {
				t.Errorf("%s file under kind %s: allowed=%t", from.name, to.name, ok)
			}
		}
	}
}

// TestHarnessReadersRefuseCredentialFiles reproduces the review case: the
// Claude title reader, the Codex index reader and every usage source are
// pointed straight at synthetic credential files.
func TestHarnessReadersRefuseCredentialFiles(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, name := range []string{"cli-config.json", "auth.json", ".credentials.json", "cookies", "login.keychain-db"} {
		t.Run(name, func(t *testing.T) {
			home := fenceHome(t)
			path := fenceWrite(t, filepath.Join(home, name), fenceSentinel)
			o := heartbeatTestOptions(home)
			o.Harness = "claude"
			o.Transcript = path
			if label, ok := readHeartbeatNames(context.Background(), o); ok {
				t.Errorf("claude title reader opened %s: %q", name, label)
			}
			fenceWrite(t, path, `{"id":"`+id+`","thread_name":"SYNTHETIC_SENTINEL"}`+"\n")
			o.Harness, o.Transcript, o.CodexIndex, o.SourceSession = "codex", "", path, id
			if label, ok := readHeartbeatNames(context.Background(), o); ok {
				t.Errorf("codex index reader opened %s: %q", name, label)
			}
			// A symlink carrying the exact allowed name is refused too.
			index := filepath.Join(home, "codex", "session_index.jsonl")
			if err := os.MkdirAll(filepath.Dir(index), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, index); err != nil {
				t.Fatal(err)
			}
			if _, ok := readCodexSessionLabel(context.Background(), index, id); ok {
				t.Errorf("codex index reader followed a symlink to %s", name)
			}
			for _, source := range []string{"claude", "codex", "grok", "cursor"} {
				if _, _, _, _, err := scanUsageWindowSource(context.Background(), path, "gpt-5", 0, 1<<20, nil, false, source); err == nil {
					t.Errorf("%s usage scan accepted %s", source, name)
				}
			}
			if _, err := readGrokUsage(path, "grok-4"); err == nil {
				t.Errorf("grok usage reader accepted %s", name)
			}
		})
	}
	// The review's relative case: a Grok usage path whose real ancestor is Keychains.
	home := fenceHome(t)
	dir := filepath.Join(home, "Keychains")
	fenceWrite(t, filepath.Join(dir, "sessions", "work", "abcdefgh", "usage.json"), `{"session":{"inputTokens":1,"outputTokens":1,"primaryModelId":"grok-4"}}`)
	t.Chdir(dir)
	if lines, err := readGrokUsage("sessions/work/abcdefgh/usage.json", ""); err == nil || len(lines) > 0 {
		t.Fatal("relative usage path opened inside a Keychains ancestor")
	}
}

// TestHarnessReadersUseTheFence keeps every harness reader on the fence: no
// heartbeat source file may open, read or stat a file any other way.
func TestHarnessReadersUseTheFence(t *testing.T) {
	files, err := filepath.Glob("harness_heartbeat*.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`\bos\.(Open|OpenFile|ReadFile|Stat|Lstat)\(|\bopenNoFollow\(`)
	allowed := map[string]bool{
		// Definitions of the fence mechanics and the private state directory.
		"harness_heartbeat_unix.go":  true,
		"harness_heartbeat_other.go": true,
		// /proc process identity, not a harness file.
		"harness_heartbeat_linux.go": true,
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if direct.MatchString(line) && !strings.Contains(line, "os.Lstat(root)") {
				t.Errorf("%s:%d reads a file outside openHarnessFile: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Round 3 review case: names a case- and normalization-insensitive
// filesystem resolves to a denied name must be denied too, whatever the
// spelling: long s, Kelvin sign, NFD, fullwidth, zero-width characters.
func TestHarnessFenceFilesystemEquivalentNames(t *testing.T) {
	denied := []string{
		"\u017fecrets", ".\u017fsh", ".\u017f\u017fh", "auth.j\u017fon", "\u212aeychains", "\uff2beychains", "\uff21\uff35\uff34\uff28.json", "\uff53\uff45\uff43\uff52\uff45\uff54\uff53",
		"KEYCHAINS", "kEyChAiNs", "AUTH.JSON", "CLI-CONFIG.JSON", "Key\u200bchains", "Keycha\u034fins", "Keycha\u200cins",
		"Keychains\u200d", "Keychains\ufeff", "auth.jso\u200cn", "Cooki\u0301es", "Se\u0301crets",
	}
	for _, tc := range fenceKinds {
		t.Run(tc.name, func(t *testing.T) {
			home := fenceHome(t)
			for _, name := range denied {
				p := fenceWrite(t, filepath.Join(home, name, tc.rel), fenceSentinel)
				if f, err := openHarnessFile(tc.kind, p); err == nil {
					f.Close()
					t.Errorf("opened beneath %q", name)
				}
			}
			// Real aliases on this filesystem: whatever name the kernel maps
			// onto a denied directory is refused.
			for _, canonical := range []string{"Keychains", "secrets", ".ssh", "auth.json"} {
				real := fenceWrite(t, filepath.Join(home, "alias", canonical, tc.rel), fenceSentinel)
				want, err := os.Stat(real)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range denied {
					alias := filepath.Join(home, "alias", name, tc.rel)
					if got, err := os.Stat(alias); err == nil && os.SameFile(want, got) {
						if f, err := openHarnessFile(tc.kind, alias); err == nil {
							f.Close()
							t.Errorf("filesystem alias %q of %q opened", name, canonical)
						}
					}
				}
			}
		})
	}
}

// Inside the vendor shape every component is plain ASCII, so no Unicode
// spelling can alias another name there; a non-ASCII home above the anchor
// stays usable.
func TestHarnessFenceShapeIsASCII(t *testing.T) {
	for _, tc := range fenceKinds {
		t.Run(tc.name, func(t *testing.T) {
			for _, home := range []string{"caf\u00e9", "cafe\u0301", "\u041aeychains-free"} {
				p := fenceWrite(t, filepath.Join(fenceHome(t), home, tc.rel), fenceSentinel)
				f, err := openHarnessFile(tc.kind, p)
				if err != nil {
					t.Fatalf("non-ASCII ancestor %q refused: %v", home, err)
				}
				f.Close()
			}
			dir, base := filepath.Split(tc.rel)
			for _, bad := range []string{
				filepath.Join(dir, "\uff21"+base),
				filepath.Join(dir, strings.ReplaceAll(base, ".", "\uff0e")),
				filepath.Join(dir, strings.Replace(base, "s", "\u017f", 1)),
			} {
				if bad == tc.rel {
					continue
				}
				p := fenceWrite(t, filepath.Join(fenceHome(t), bad), fenceSentinel)
				if f, err := openHarnessFile(tc.kind, p); err == nil {
					f.Close()
					t.Errorf("lookalike vendor name %q opened", bad)
				}
			}
			if strings.Count(tc.rel, "/") >= 3 {
				parts := strings.Split(tc.rel, "/")
				parts[len(parts)-2] = "se\u0301ssion"
				p := fenceWrite(t, filepath.Join(fenceHome(t), filepath.Join(parts...)), fenceSentinel)
				if f, err := openHarnessFile(tc.kind, p); err == nil {
					f.Close()
					t.Errorf("non-ASCII component inside the vendor shape opened: %q", p)
				}
			}
		})
	}
}

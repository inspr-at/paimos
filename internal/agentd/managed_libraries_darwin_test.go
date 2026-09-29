// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin && !aeon_test_unsupported

package agentd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalDynamicLibraryFixture(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("C compiler unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "work")
	bin := filepath.Join(root, "Cellar/app/1/bin")
	lib := filepath.Join(root, "Cellar/dependency/1/lib")
	for _, dir := range []string{workspace, bin, lib} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	opt := filepath.Join(root, "opt")
	if err := os.Symlink(filepath.Dir(lib), opt); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(lib, "libleaf.dylib")
	parent := filepath.Join(lib, "libparent.dylib")
	sibling := filepath.Join(lib, "unrelated-fixture")
	if err := os.WriteFile(sibling, []byte("not a library"), 0600); err != nil {
		t.Fatal(err)
	}
	compile := func(name, source string, args ...string) {
		t.Helper()
		src := filepath.Join(root, name+".c")
		if err := os.WriteFile(src, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), "/usr/bin/clang", append([]string{src}, args...)...)
		cmd.Dir = root
		cmd.Env = terminalEnvironment(root, "off", "", "/usr/bin/clang")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compile fixture: %v %s", err, out)
		}
	}
	compile("leaf", "int leaf(void) { return 42; }", "-dynamiclib", "-Wl,-install_name,@loader_path/libleaf.dylib", "-o", leaf)
	compile("parent", "extern int leaf(void); int parent(void) { return leaf(); }", leaf, "-dynamiclib", "-Wl,-install_name,@rpath/libparent.dylib", "-o", parent)
	binary := filepath.Join(bin, "fixture")
	compile("main", fmt.Sprintf(`#include <fcntl.h>
#include <stdio.h>
#include <unistd.h>
extern int parent(void);
int main(void) {
 if (parent() != 42) return 1;
 if (access(%q, R_OK) != 0) return 2;
 if (open(%q, O_RDONLY) >= 0) return 3;
 if (open(%q, O_WRONLY) >= 0) return 4;
 puts("sandbox-library-ok"); return 0;
}`, leaf, sibling, leaf), parent, "-Wl,-rpath,"+opt+"/lib", "-o", binary)
	var cache terminalLibraryCache
	files, err := cache.closure(t.Context(), workspace, root, binary, inspectTerminalLibrary)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("fixture closure: %v", files)
	}
	// The writable scratch tree is separate from the fixture installation.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := terminalSandboxProfile(workspace, tmp, nil, []string{binary}, files)
	out, err := runSandboxedTerminal(t.Context(), workspace, profile, binary, terminalEnvironment(tmp, "off", "", binary), nil)
	if err != nil || !strings.Contains(out, "sandbox-library-ok") {
		t.Fatalf("fixture library sandbox: %v %s", err, out)
	}
}

func TestTerminalHomebrewNode(t *testing.T) {
	var prefix string
	for _, candidate := range []string{"/opt/homebrew", "/usr/local"} {
		node, err := filepath.EvalSymlinks(filepath.Join(candidate, "bin/node"))
		if err == nil && strings.HasPrefix(node, candidate+"/Cellar/") {
			prefix = candidate
			break
		}
	}
	if prefix == "" {
		t.Skip("Homebrew node unavailable")
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin/npm")); err != nil {
		t.Skip("Homebrew npm unavailable")
	}
	t.Setenv("PATH", prefix+"/bin:/usr/bin:/bin")
	workspace := t.TempDir()
	web := filepath.Join(workspace, "web")
	if err := os.Mkdir(web, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "package.json"), []byte(`{"scripts":{"test":"node test.js"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`const fs = require("fs");
try {
  const fd = fs.openSync(%q, "r");
  fs.closeSync(fd);
  process.exit(1);
} catch (error) {
  if (!["ENOENT", "EPERM", "EACCES"].includes(error.code)) throw error;
}
fs.writeFileSync("node-result", "ok");
`, filepath.Join(prefix, "etc/openssl@3/openssl.cnf"))
	if err := os.WriteFile(filepath.Join(web, "test.js"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := runTerminal(t.Context(), workspace, "", terminalArgs{Command: "npm", Args: []string{"run", "test"}, Directory: "web"}); err != nil {
		t.Fatalf("Homebrew npm/node: %v\n%s", err, out)
	}
	if content, err := os.ReadFile(filepath.Join(web, "node-result")); err != nil || string(content) != "ok" {
		t.Fatalf("Homebrew node result: %q %v", content, err)
	}
}

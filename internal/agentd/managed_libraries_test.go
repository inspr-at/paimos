// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTerminalLibraryClosureAndCache(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "Cellar/node/1/bin/node")
	a := filepath.Join(root, "Cellar/a/1/lib/a.dylib")
	b := filepath.Join(root, "Cellar/a/1/lib/b.dylib")
	c := filepath.Join(root, "Cellar/node/1/lib/c.dylib")
	for _, path := range []string{binary, a, b, c} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	opt := filepath.Join(root, "opt")
	if err := os.Symlink(filepath.Join(root, "Cellar/a/1"), opt); err != nil {
		t.Fatal(err)
	}
	images := map[string]terminalLibraryInfo{
		binary: {dependencies: []string{"@rpath/a.dylib", "/usr/lib/libSystem.B.dylib"}, rpaths: []string{opt + "/lib", "@loader_path/../lib"}},
		a:      {dependencies: []string{"@loader_path/b.dylib"}},
		b:      {dependencies: []string{"@rpath/c.dylib"}},                            // Inherits the executable's search stack.
		c:      {dependencies: []string{"@executable_path/../../../a/1/lib/a.dylib"}}, // Cycle.
	}
	queries := 0
	inspect := func(_ context.Context, _, path string) (terminalLibraryInfo, error) {
		queries++
		info, ok := images[path]
		if !ok {
			t.Fatalf("unexpected image %s", path)
		}
		return info, nil
	}
	var cache terminalLibraryCache
	resolve := func() []string {
		files, err := cache.closure(t.Context(), filepath.Join(root, "work"), root, binary, inspect)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(files, []string{opt + "/lib/a.dylib", a, opt + "/lib/b.dylib", b, c}) {
			t.Fatalf("closure: %v", files)
		}
		return files
	}
	files := resolve()
	files[0] = "caller mutation"
	resolve()
	if queries != 4 {
		t.Fatalf("cache miss: %d inspections", queries)
	}
	// Mtime is part of the cache key even when bytes and size stay unchanged.
	now := time.Now().Add(time.Hour)
	for i, path := range []string{binary, b} {
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
		resolve()
		if queries != 4*(i+2) {
			t.Fatal("changed executable/dependency reused stale closure")
		}
	}
	// A cached closure must still enforce the current workspace boundary.
	if _, err := cache.closure(t.Context(), filepath.Dir(a), root, binary, inspect); err == nil {
		t.Fatal("workspace dependency accepted through cache")
	}
	// Retarget an opt alias without changing the binary or old dylib.
	a2 := filepath.Join(root, "Cellar/a/2/lib/a.dylib")
	if err := os.MkdirAll(filepath.Dir(a2), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a2, []byte("fixture2"), 0600); err != nil {
		t.Fatal(err)
	}
	newOpt := opt + "-new"
	if err := os.Symlink(filepath.Join(root, "Cellar/a/2"), newOpt); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newOpt, opt); err != nil {
		t.Fatal(err)
	}
	images[a2] = terminalLibraryInfo{}
	got, err := cache.closure(t.Context(), filepath.Join(root, "work"), root, binary, inspect)
	if err != nil || !reflect.DeepEqual(got, []string{opt + "/lib/a.dylib", a2}) {
		t.Fatalf("opt retarget kept stale closure: %v %v", got, err)
	}
}

func TestTerminalLibraryClosureFailsClosed(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "binary")
	if err := os.WriteFile(binary, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dependency := range []string{"@rpath/missing.dylib", "relative.dylib", root, filepath.Join(root, "missing"), "@unknown/a"} {
		t.Run(dependency, func(t *testing.T) {
			var cache terminalLibraryCache
			inspect := func(context.Context, string, string) (terminalLibraryInfo, error) {
				return terminalLibraryInfo{dependencies: []string{dependency}}, nil
			}
			if _, err := cache.closure(t.Context(), filepath.Join(root, "work"), root, binary, inspect); err == nil {
				t.Fatal("unresolved or non-file dependency admitted")
			}
			if len(cache.entries) != 0 {
				t.Fatal("failed closure was cached")
			}
		})
	}
}

func TestParseTerminalLibraries(t *testing.T) {
	linked := `fixture:
	@rpath/self.dylib (compatibility version 1.0.0, current version 1.0.0)
	/opt/homebrew/opt/a/lib/with spaces.dylib (compatibility version 1.0.0, current version 1.0.0)
	@loader_path/next.dylib (compatibility version 1.0.0, current version 1.0.0)
`
	commands := `Load command 0
 cmd LC_ID_DYLIB
 name @rpath/self.dylib (offset 24)
Load command 1
 cmd LC_RPATH
 path @loader_path/../lib (offset 12)
Load command 2
 cmd LC_RPATH
 path /path with spaces (offset 12)
`
	want := terminalLibraryInfo{
		dependencies: []string{"/opt/homebrew/opt/a/lib/with spaces.dylib", "@loader_path/next.dylib"},
		rpaths:       []string{"@loader_path/../lib", "/path with spaces"},
	}
	if got := parseTerminalLibraries(linked, commands); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed library commands: %#v", got)
	}
	var out terminalLibraryOutput
	if _, err := fmt.Fprint(&out, strings.Repeat("x", 1<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("x")); err == nil {
		t.Fatal("inspection output was unbounded")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRenderedIgnoresPlantedTemp(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "rendered")
			victim := filepath.Join(t.TempDir(), "victim")
			const original = "do not overwrite"
			if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			planted := path + ".tmp"
			if symlink {
				if err := os.Symlink(victim, planted); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(planted, []byte(original), 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(planted, 0o666); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeRenderedInWorkspace(dir, path, "new body"); err != nil {
				t.Fatal(err)
			}
			assertRenderedFile(t, path, "new body")
			for _, untouched := range []string{victim, planted} {
				body, err := os.ReadFile(untouched)
				if err != nil || string(body) != original {
					t.Fatalf("planted path or victim changed: %s, %q, %v", untouched, body, err)
				}
			}
			info, err := os.Lstat(planted)
			if err != nil {
				t.Fatal(err)
			}
			if symlink && info.Mode()&os.ModeSymlink == 0 || !symlink && info.Mode().Perm() != 0o666 {
				t.Fatalf("planted entry changed: %v", info.Mode())
			}
			assertNoRenderedTemps(t, dir)
		})
	}
}

func TestWriteRenderedRefusesSymlinkDestination(t *testing.T) {
	for _, target := range []string{"absolute", "relative", "dangling", "inside"} {
		t.Run(target, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "output")
			if err := os.Mkdir(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(base, "victim")
			if target == "inside" {
				victim = filepath.Join(dir, "victim")
			}
			if target != "dangling" {
				if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := victim
			if target == "relative" {
				link = filepath.Join("..", "victim")
			}
			path := filepath.Join(dir, "rendered")
			if err := os.Symlink(link, path); err != nil {
				t.Fatal(err)
			}
			if err := writeRenderedInWorkspace(base, path, "replacement"); err == nil || !strings.Contains(err.Error(), "symlink destination") {
				t.Fatalf("expected symlink refusal, got %v", err)
			}
			if got, err := os.Readlink(path); err != nil || got != link {
				t.Fatalf("destination symlink changed: %q, %v", got, err)
			}
			if target == "dangling" {
				if _, err := os.Lstat(victim); !os.IsNotExist(err) {
					t.Fatalf("dangling target was created: %v", err)
				}
			} else {
				assertRenderedFile(t, victim, "original")
			}
			assertNoRenderedTemps(t, dir)
		})
	}
}

func TestWriteRenderedConcurrentAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rendered")
	const writers = 24
	bodies := make(map[string]bool, writers+1)
	bodies["initial"] = true
	for i := range writers {
		bodies[strings.Repeat(fmt.Sprintf("writer %02d\n", i), 2048)] = true
	}
	if err := writeRenderedInWorkspace(dir, path, "initial"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	ready := make(chan struct{}, writers)
	done := make(chan struct{})
	results := make(chan error, writers)
	observed := make(chan error, 1)
	// A reader must always see an entire published body, never a missing file
	// or a partially written mixture. Channels coordinate the run; no sleeps.
	go func() {
		<-start
		for {
			body, err := os.ReadFile(path)
			if err != nil || !bodies[string(body)] {
				observed <- fmt.Errorf("non-atomic output: length=%d, error=%v", len(body), err)
				return
			}
			select {
			case <-done:
				observed <- nil
				return
			default:
			}
		}
	}()
	for body := range bodies {
		if body == "initial" {
			continue
		}
		go func() {
			ready <- struct{}{}
			<-start
			results <- writeRenderedInWorkspace(dir, path, body)
		}()
	}
	for range writers {
		<-ready
	}
	close(start)
	for range writers {
		if err := <-results; err != nil {
			t.Errorf("concurrent render: %v", err)
		}
	}
	close(done)
	if err := <-observed; err != nil {
		t.Error(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) == "initial" || !bodies[string(body)] {
		t.Fatalf("final output is not a writer's complete body: length=%d, error=%v", len(body), err)
	}
	assertRenderedFile(t, path, string(body))
	assertNoRenderedTemps(t, dir)
}

func TestWriteRenderedCreatesParentsAndReplacesPrivately(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "nested", "output", "rendered")
	if err := writeRenderedInWorkspace(workspace, path, "first"); err != nil {
		t.Fatal(err)
	}
	assertRenderedFile(t, path, "first")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := writeRenderedInWorkspace(workspace, path, "second"); err != nil {
		t.Fatal(err)
	}
	assertRenderedFile(t, path, "second")
	assertNoRenderedTemps(t, filepath.Dir(path))
}

func TestWriteRenderedCleansUpOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rendered")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeRenderedInWorkspace(dir, path, "body"); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("expected rename failure, got %v", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("destination directory changed: %v", err)
	}
	assertNoRenderedTemps(t, dir)
}

func TestWriteRenderedRefusesAncestorSymlink(t *testing.T) {
	for _, ancestor := range []string{".agents", filepath.Join(".agents", "skills", "ops")} {
		for _, target := range []string{"existing", "missing-parents", "dangling"} {
			t.Run(ancestor+"/"+target, func(t *testing.T) {
				workspace, outside := t.TempDir(), t.TempDir()
				link := filepath.Join(workspace, ancestor)
				if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
					t.Fatal(err)
				}
				victim := filepath.Join(outside, "rendered")
				if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				linkTarget := outside
				if target == "dangling" {
					linkTarget = filepath.Join(outside, "missing")
				}
				if err := os.Symlink(linkTarget, link); err != nil {
					t.Fatal(err)
				}
				suffix := "rendered"
				if target == "missing-parents" {
					suffix = filepath.Join("new", "nested", "rendered")
				}
				path, err := resolveSkillPath("", workspace, filepath.Join(ancestor, suffix))
				if err != nil {
					t.Fatal(err)
				}
				if err := writeRenderedInWorkspace(workspace, path, "replacement"); err == nil || !strings.Contains(err.Error(), "without following symlinks") {
					t.Errorf("expected ancestor symlink refusal, got %v", err)
				}
				assertRenderedFile(t, victim, "original")
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 1 || entries[0].Name() != "rendered" {
					t.Fatalf("outside directory changed: %v, %v", entries, err)
				}
				if got, err := os.Readlink(link); err != nil || got != linkTarget {
					t.Fatalf("ancestor symlink changed: %q, %v", got, err)
				}
			})
		}
	}
}

func TestWriteRenderedIndexRefusesAncestorSymlink(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	const original = `{"schema_version":"1","entries":[]}`
	victim := filepath.Join(outside, "rendered-skills.json")
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".paimos")); err != nil {
		t.Fatal(err)
	}
	err := upsertRenderedSkill(workspace, renderedSkillEntry{Project: "AEON", Agent: "ops", Harness: "codex", Path: ".agents/skills/ops/SKILL.md"})
	if err == nil || !strings.Contains(err.Error(), "without following symlinks") {
		t.Errorf("expected index ancestor symlink refusal, got %v", err)
	}
	assertRenderedFile(t, victim, original)
	assertNoRenderedTemps(t, outside)
}

func TestWriteRenderedExplicitOutsideWorkspace(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	for _, relative := range []bool{false, true} {
		t.Run(fmt.Sprintf("relative=%t", relative), func(t *testing.T) {
			out := filepath.Join(outside, "nested", "rendered")
			if relative {
				var err error
				out, err = filepath.Rel(workspace, out)
				if err != nil {
					t.Fatal(err)
				}
			}
			path, err := resolveSkillPath(out, workspace, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRenderedInWorkspace(workspace, path, "explicit output"); err != nil {
				t.Fatal(err)
			}
			assertRenderedFile(t, path, "explicit output")
			assertNoRenderedTemps(t, filepath.Dir(path))
		})
	}
}

func assertRenderedFile(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatalf("%s: content mismatch, error=%v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s: expected regular private file, mode=%v", path, info.Mode())
	}
}

func assertNoRenderedTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".paimos-render-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

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
			if err := writeRendered(path, "new body"); err != nil {
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
			if err := writeRendered(path, "replacement"); err == nil || !strings.Contains(err.Error(), "symlink destination") {
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
	if err := writeRendered(path, "initial"); err != nil {
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
			results <- writeRendered(path, body)
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
	path := filepath.Join(t.TempDir(), "nested", "output", "rendered")
	if err := writeRendered(path, "first"); err != nil {
		t.Fatal(err)
	}
	assertRenderedFile(t, path, "first")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := writeRendered(path, "second"); err != nil {
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
	if err := writeRendered(path, "body"); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("expected rename failure, got %v", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("destination directory changed: %v", err)
	}
	assertNoRenderedTemps(t, dir)
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

// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shortSocketHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "aeon-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSocketPathDefaultAndFallbackByteLimits(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := "/Users/" + strings.Repeat("u", 32)
			root, err := DefaultStateRoot(goos, home, "")
			if err != nil {
				t.Fatal(err)
			}
			limit := 100
			if goos == "linux" {
				limit = 104
			}
			path, err := resolveSocketPath(goos, home, filepath.Join(root, "daemon"), nil)
			if err != nil || len(path) > limit {
				t.Fatalf("default socket = %q: %v", path, err)
			}
			for _, n := range []int{limit, limit + 1} {
				state := "/" + strings.Repeat("s", n-len("/agentd.sock")-1)
				path, err := resolveSocketPath(goos, home, state, nil)
				if err != nil || len(path) > limit {
					t.Fatalf("boundary %d: %q %v", n, path, err)
				}
				fallback := strings.HasPrefix(path, filepath.Join(home, ".aeon", "run")+"/")
				if fallback != (n > limit) {
					t.Fatalf("fallback at %d = %v", n, fallback)
				}
				ref := &ControlReference{Socket: path, DaemonID: "daemon", Generation: strings.Repeat("a", 32)}
				client, err := resolveSocketPath(goos, home, state, ref)
				if err != nil || client != path {
					t.Fatalf("client and serve differ: %q %q %v", client, path, err)
				}
			}
			// Multibyte paths are measured in bytes, not username characters.
			var tooLong *SocketPathLengthError
			if _, err := resolveSocketPath(goos, "/Users/"+strings.Repeat("é", 64), "/"+strings.Repeat("s", 150), nil); !errors.As(err, &tooLong) {
				t.Fatalf("impossible path: %v", err)
			}
			a, _ := resolveSocketPath(goos, home, "/"+strings.Repeat("a", 150), nil)
			b, _ := resolveSocketPath(goos, home, "/"+strings.Repeat("b", 150), nil)
			if a == b {
				t.Fatal("different roots share a fallback")
			}
		})
	}
}

func TestSocketPathNeedsHomeOnlyForFallback(t *testing.T) {
	t.Setenv("HOME", "")
	state := "/private/aeon-state/daemon"
	ref := &ControlReference{Socket: "agentd.sock", DaemonID: "daemon", Generation: strings.Repeat("a", 32)}
	for _, reference := range []*ControlReference{nil, ref} {
		path, err := ResolveSocketPath(state, reference)
		if err != nil || path != filepath.Join(state, "agentd.sock") {
			t.Fatalf("short path without HOME: %q %v", path, err)
		}
	}
	if _, err := ResolveSocketPath("/"+strings.Repeat("s", 150), nil); err == nil || !strings.Contains(err.Error(), "fallback requires a user home") {
		t.Fatalf("fallback without HOME: %v", err)
	}
}

func TestSocketPathLegacyDiscoveryAndReferenceRejection(t *testing.T) {
	home := shortSocketHome(t)
	state := filepath.Join(home, "daemon")
	s, err := OpenStore(state, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	ref := &ControlReference{DaemonID: "daemon", Generation: strings.Repeat("a", 32)}
	ref.Socket = "agentd-" + ref.Generation + ".sock"
	legacy := filepath.Join(state, ref.Socket)
	l, err := net.Listen("unix", legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(legacy, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := resolveSocketPath("darwin", home, state, ref)
	if err != nil || path != legacy {
		t.Fatalf("legacy discovery: %q %v", path, err)
	}
	t.Setenv("HOME", "")
	if path, err := ResolveSocketPath(state, ref); err != nil || path != legacy {
		t.Fatalf("legacy discovery without HOME: %q %v", path, err)
	}
	l.Close()
	path, err = resolveSocketPath("darwin", home, state, ref)
	if err != nil || path != filepath.Join(state, "agentd.sock") {
		t.Fatalf("retired legacy: %q %v", path, err)
	}
	for _, bad := range []string{"../agentd.sock", "/tmp/agentd.sock", "agentd-" + strings.Repeat("b", 32) + ".sock"} {
		ref.Socket = bad
		if _, err := resolveSocketPath("darwin", home, state, ref); err == nil {
			t.Fatalf("unrecognized reference accepted: %q", bad)
		}
	}
	ref.Socket, ref.Generation = "agentd.sock", "bad"
	if _, err := resolveSocketPath("darwin", home, state, ref); err == nil {
		t.Fatal("invalid generation accepted")
	}
}

func TestSocketFallbackRealBindAndPrivateDirectory(t *testing.T) {
	home := shortSocketHome(t)
	state := filepath.Join(home, strings.Repeat("long-state-", 10), "daemon")
	path, err := resolveSocketPath("darwin", home, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocketDirectory(path); err != nil {
		t.Fatal(err)
	}
	for dir := filepath.Dir(path); dir != home; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("fallback directory is not private: %v %v", info, err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("real bind (path %d bytes): %v", len(path), err)
	}
	defer l.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSocket(path); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocketDirectory(path); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("public run directory accepted: %v", err)
	}
}

func TestSocketLegacyListenerWithinOSLimitBeyondNewPathMargin(t *testing.T) {
	home := shortSocketHome(t)
	ref := &ControlReference{DaemonID: "daemon", Generation: strings.Repeat("a", 32)}
	ref.Socket = "agentd-" + ref.Generation + ".sock"
	// 103 bytes is valid on Darwin, despite the 100-byte new-path budget.
	state := home + "/" + strings.Repeat("s", 103-len(home)-1-len("/"+ref.Socket))
	s, err := OpenStore(state, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	legacy := filepath.Join(state, ref.Socket)
	l, err := net.Listen("unix", legacy)
	if err != nil {
		t.Fatalf("103-byte legacy bind: %v", err)
	}
	defer l.Close()
	if err := os.Chmod(legacy, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := resolveSocketPath("darwin", home, state, ref)
	if err != nil || path != legacy {
		t.Fatalf("working legacy listener abandoned: %q %v", path, err)
	}
}

func TestSocketFallbackRejectsSymlinkAncestors(t *testing.T) {
	for _, component := range []string{".aeon", "run"} {
		t.Run(component, func(t *testing.T) {
			home, other := shortSocketHome(t), shortSocketHome(t)
			link := filepath.Join(home, ".aeon")
			if component == "run" {
				if err := os.Mkdir(link, 0700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, "run")
			}
			if err := os.Symlink(other, link); err != nil {
				t.Fatal(err)
			}
			path, err := resolveSocketPath("darwin", home, "/"+strings.Repeat("s", 150), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := PrepareSocketDirectory(path); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("symlink accepted: %v", err)
			}
			entries, err := os.ReadDir(other)
			if err != nil || len(entries) != 0 {
				t.Fatal("followed fallback symlink")
			}
		})
	}
}

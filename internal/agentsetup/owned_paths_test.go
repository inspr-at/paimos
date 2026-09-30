// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

type pathInfoFixture struct {
	os.FileInfo
	mode     os.FileMode
	uid, gid uint32
}

func (f pathInfoFixture) Mode() os.FileMode { return f.mode }
func (f pathInfoFixture) IsDir() bool       { return f.mode.IsDir() }
func (f pathInfoFixture) Sys() any          { return &syscall.Stat_t{Uid: f.uid, Gid: f.gid} }

func TestOwnedPathPermissions(t *testing.T) {
	uid := uint32(os.Getuid())
	for _, tc := range []struct {
		name     string
		mode     os.FileMode
		uid, gid uint32
		ok       bool
	}{
		{"admin user directory", os.ModeDir | 0775, uid, 80, runtime.GOOS == "darwin"},
		{"admin root directory", os.ModeDir | 0775, 0, 80, runtime.GOOS == "darwin"},
		{"admin foreign owner", os.ModeDir | 0775, uid + 10000, 80, false},
		{"other group", os.ModeDir | 0775, uid, 20, false},
		{"world writable", os.ModeDir | 0777, uid, 80, false},
		{"group writable leaf", 0775, uid, 80, false},
		{"world writable leaf", 0757, uid, 80, false},
		{"sticky leaf", os.ModeSticky | 0777, uid, 80, false},
		{"sticky ancestor", os.ModeDir | os.ModeSticky | 0777, 0, 0, true},
		{"owned directory", os.ModeDir | 0755, uid, 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ownedComponentError("/fixture/component", pathInfoFixture{mode: tc.mode, uid: tc.uid, gid: tc.gid}, true)
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v, want %v: %v", err == nil, tc.ok, err)
			}
			if err != nil && (!errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "/fixture/component")) {
				t.Fatal("missing path or unsafe sentinel", err)
			}
		})
	}
}

func TestHomebrewRepositoryAllowlistAndMetadata(t *testing.T) {
	uid := uint32(os.Getuid())
	for _, prefix := range []string{"/opt/homebrew", "/usr/local/Homebrew", "/home/linuxbrew/.linuxbrew", "/opt/homebrew-copy", "/usr/local", "/tmp/Homebrew", "/opt/homebrew/Cellar/project"} {
		p := installedPathPolicy()
		p.lstat = func(path string) (os.FileInfo, error) {
			if path == prefix || path == filepath.Join(prefix, ".git") {
				return pathInfoFixture{mode: os.ModeDir | 0755, uid: uid}, nil
			}
			return nil, os.ErrNotExist
		}
		want := prefix == "/opt/homebrew" || prefix == "/usr/local/Homebrew" || prefix == "/home/linuxbrew/.linuxbrew"
		if got := p.repositoryError(filepath.Join(prefix, "bin", "node")); (got == nil) != want {
			t.Fatalf("prefix %s accepted=%v, want %v: %v", prefix, got == nil, want, got)
		}
	}
	for _, part := range []string{"/opt/homebrew", "/opt/homebrew/.git"} {
		for _, bad := range []pathInfoFixture{
			{mode: os.ModeDir | 0755, uid: uid + 10000},
			{mode: os.ModeDir | 0775, uid: uid, gid: 20},
			{mode: os.ModeDir | os.ModeSticky | 0777, uid: uid, gid: 80},
			{mode: os.ModeSymlink | 0777, uid: uid},
		} {
			p := installedPathPolicy()
			p.lstat = func(path string) (os.FileInfo, error) {
				if path == part {
					return bad, nil
				}
				if path == "/opt/homebrew" || path == "/opt/homebrew/.git" {
					return pathInfoFixture{mode: os.ModeDir | 0755, uid: uid}, nil
				}
				return nil, os.ErrNotExist
			}
			if err := p.repositoryError("/opt/homebrew/bin/node"); err == nil || !strings.Contains(err.Error(), part) {
				t.Fatal("unsafe Homebrew metadata accepted or unidentified", part, err)
			}
		}
	}
}

func TestHomebrewLayoutResolution(t *testing.T) {
	root := physicalTemp(t)
	prefix := filepath.Join(root, "homebrew")
	node := filepath.Join(prefix, "Cellar", "node", "X", "bin", "node")
	sdk := filepath.Join(prefix, "Cellar", "node", "X", "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk", "sdk.mjs")
	for _, dir := range []string{filepath.Join(prefix, ".git"), filepath.Join(prefix, "bin"), filepath.Join(prefix, "links"), filepath.Dir(node), filepath.Dir(sdk)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{node, sdk} {
		if err := os.WriteFile(path, []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(prefix, "bin", "node")
	if err := os.Symlink("../links/node", link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/node/X/bin/node", filepath.Join(prefix, "links", "node")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		// Real macOS metadata: no system prefix is written, and chown changes
		// only the group of this user's temporary fixture directories.
		for _, dir := range []string{prefix, filepath.Join(prefix, ".git"), filepath.Join(prefix, "bin"), filepath.Join(prefix, "links"), filepath.Join(prefix, "Cellar"), filepath.Dir(sdk)} {
			if err := os.Chown(dir, -1, 80); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0775); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := installedPathPolicy()
	// Substitute only the exact fixture prefix, keeping the production traversal.
	p.homebrewPrefix = func(path string) bool { return path == prefix }
	for _, path := range []string{link, node, sdk} {
		if _, err := p.pinnedRegular(path, "", path != sdk); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pinnedRegular(path, prefix, path != sdk); err == nil {
			t.Fatal("Homebrew exemption bypassed the workspace boundary")
		}
	}
	for _, harness := range []string{"claude", "codex", "cursor-agent"} {
		launcher := filepath.Join(prefix, "bin", harness)
		if err := os.Symlink("../Cellar/node/X/bin/node", launcher); err != nil {
			t.Fatal(err)
		}
		if got, err := p.pinnedRegular(launcher, "", true); err != nil || got != node {
			t.Fatal("launcher resolution failed", harness, err)
		}
	}
	for _, bad := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "links"), node, sdk} {
		info, err := os.Stat(bad)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(bad, 0777); err != nil {
			t.Fatal(err)
		}
		path := link
		if bad == sdk {
			path = sdk
		}
		if _, err := p.pinnedRegular(path, "", false); err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatal("unsafe traversed component not identified", bad, err)
		}
		if err := os.Chmod(bad, info.Mode()); err != nil {
			t.Fatal(err)
		}
	}
	foreign := p
	foreign.lstat = func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err == nil && path == filepath.Join(prefix, ".git") {
			return pathInfoFixture{FileInfo: info, mode: info.Mode(), uid: uint32(os.Getuid() + 10000), gid: 80}, nil
		}
		return info, err
	}
	if _, err := foreign.pinnedRegular(link, "", true); err == nil || !strings.Contains(err.Error(), filepath.Join(prefix, ".git")) {
		t.Fatal("foreign-owned .git accepted", err)
	}
	for _, ancestor := range []string{root, filepath.Dir(node)} {
		git := filepath.Join(ancestor, ".git")
		if err := os.Mkdir(git, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pinnedRegular(link, "", true); err == nil {
			t.Fatal("another .git ancestor accepted")
		}
		if err := os.Rename(git, git+"-retired"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeDependencyErrorNamesUnsafeComponent(t *testing.T) {
	_, node, sdk := claudeFixture(t)
	for _, tc := range []struct{ path, prefix string }{
		{filepath.Dir(node), "Claude Node executable is unsafe or unavailable"},
		{filepath.Dir(sdk), "Claude Agent SDK module is unsafe or unavailable"},
	} {
		if err := os.Chmod(tc.path, 0777); err != nil {
			t.Fatal(err)
		}
		_, err := (Discovery{}).ResolveClaudeDependencies(ClaudeDependencies{NodePath: node, SDKPath: sdk}, "")
		if err == nil || !strings.HasPrefix(err.Error(), tc.prefix) || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), "world-writable") || !strings.Contains(err.Error(), "remove write access") {
			t.Fatal("dependency diagnostic missing path, reason, or hint", err)
		}
		if tc.path == filepath.Dir(sdk) {
			_, err := (Discovery{}).ResolveClaudeDependencies(ClaudeDependencies{NodePath: node}, "")
			if err == nil || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), "world-writable") {
				t.Fatal("automatic SDK discovery hid unsafe installed package", err)
			}
		}
		if err := os.Chmod(tc.path, 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDarwinClaudeSDKAdminDirectories(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS admin group policy")
	}
	_, node, sdk := claudeFixture(t)
	for _, dir := range []string{filepath.Dir(node), filepath.Dir(sdk), filepath.Dir(filepath.Dir(sdk))} {
		if err := os.Chown(dir, -1, 80); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0775); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (Discovery{}).ResolveClaudeDependencies(ClaudeDependencies{NodePath: node, SDKPath: sdk}, ""); err != nil {
		t.Fatal(err)
	}
	if pathUnsafe(node, "", true) || pathUnsafe(sdk, "", false) {
		t.Fatal("safe admin directories reported unsafe")
	}
}

func TestDarwinInstalledHomebrewNode(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Homebrew smoke check")
	}
	path := "/opt/homebrew/bin/node"
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		t.Skip("Homebrew Node not installed")
	}
	if _, err := pinnedRegular(path, "", true); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxGroupWritableAncestorRefused(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux permission policy")
	}
	_, node, _ := claudeFixture(t)
	if err := os.Chmod(filepath.Dir(node), 0775); err != nil {
		t.Fatal(err)
	}
	if _, err := pinnedRegular(node, "", true); err == nil || !strings.Contains(err.Error(), "writable by group") {
		t.Fatal("Linux group-writable ancestor accepted", err)
	}
}

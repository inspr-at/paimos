// SPDX-License-Identifier: AGPL-3.0-only

package nativeprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const (
	tenantID       = "10000000-0000-4000-8000-000000000001"
	ownerID        = "20000000-0000-4000-8000-000000000001"
	accountA       = "30000000-0000-4000-8000-000000000001"
	accountB       = "30000000-0000-4000-8000-000000000002"
	accountDefault = "30000000-0000-4000-8000-000000000003"
)

// The fake retains credential sentinels and rejects every content/link access.
// Tests assert both operations and unchanged fixture state, not just an error.
type fakeFilesystem struct {
	mu          sync.Mutex
	directories map[string]Directory
	credentials map[string]string
	metadata    []string
	forbidden   []string
	ready       chan string
	release     chan struct{}
}

func (f *fakeFilesystem) Lstat(path string) (Directory, error) {
	f.mu.Lock()
	f.metadata = append(f.metadata, path)
	dir, ok := f.directories[path]
	ready, release := f.ready, f.release
	f.mu.Unlock()
	if ready != nil && (path == "/profiles/a" || path == "/profiles/b") {
		ready <- path
		<-release
	}
	if !ok {
		return Directory{}, fs.ErrNotExist
	}
	return dir, nil
}
func (f *fakeFilesystem) ReadFile(path string) ([]byte, error) { return nil, f.deny("read") }
func (f *fakeFilesystem) WriteFile(path string, body []byte, mode fs.FileMode) error {
	return f.deny("write")
}
func (f *fakeFilesystem) Symlink(old, new string) error { return f.deny("symlink") }
func (f *fakeFilesystem) deny(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forbidden = append(f.forbidden, op)
	return errors.New("credential access forbidden")
}

func profileFixture() (Config, *fakeFilesystem, nativecontract.Policy) {
	f := &fakeFilesystem{directories: map[string]Directory{}, credentials: map[string]string{
		"/owner/.claude/.credentials.json": "default-fixture-login",
		"/profiles/a/.credentials.json":    "account-a-fixture-login",
		"/profiles/b/.credentials.json":    "account-b-fixture-login",
	}}
	for _, path := range []string{"/owner", "/owner/.claude", "/profiles", "/profiles/a", "/profiles/b", "/work", "/work/project"} {
		f.directories[path] = Directory{Mode: fs.ModeDir | 0700, Owned: true, Identity: path}
	}
	scope := Scope{tenantID, ownerID, "owner-workstation"}
	binary := nativecontract.BinaryPin{Path: "/vendor/claude", SHA256: strings.Repeat("a", 64), Version: "fixture-1", Protocol: "fixture-stream-1"}
	sdk := nativecontract.BinaryPin{Path: "/vendor/sdk/bridge.mjs", SHA256: strings.Repeat("b", 64), Version: "fixture-1", Protocol: "fixture-sdk-1"}
	q := Qualification{OS: runtime.GOOS, BinarySHA256: binary.SHA256, SDKSHA256: sdk.SHA256,
		Native: true, PreserveUserHome: true, IndependentLogin: true, VendorHistory: true}
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	c := Config{UserHome: "/owner", Filesystem: f, Now: func() time.Time { return now },
		Accounts: []Enrollment{
			{Scope: scope, AccountID: accountA, AccountKey: "a", Harness: "claude", AccountHome: "/profiles/a", BindingRevision: 7, Binary: binary, SDK: sdk, Qualification: q},
			{Scope: scope, AccountID: accountB, AccountKey: "b", Harness: "claude", AccountHome: "/profiles/b", BindingRevision: 8, Binary: binary, SDK: sdk, Qualification: q},
			{Scope: scope, AccountID: accountDefault, AccountKey: "default", Harness: "claude", AccountHome: "/owner/.claude", BindingRevision: 9, Binary: binary, SDK: sdk, Qualification: q},
		},
		Workspaces: []Workspace{{Scope: scope, ID: "project", Root: "/work/project"}},
		ParentEnvironment: []string{"HOME=/ambient", "CLAUDE_CONFIG_DIR=/ambient/claude", "CODEX_HOME=/ambient/codex",
			"USER=fixture-user", "LOGNAME=fixture-user", "TMPDIR=/scratch", "PATH=/ambient/bin", "NODE_OPTIONS=fixture-loader",
			"ANTHROPIC_API_KEY=fixture-api-key", "AEON_AGENT_KEY=fixture-agent-key", "CLAUDE_CODE_USE_BEDROCK=1"},
	}
	// The normal default login does not claim independent dedicated-profile
	// isolation. It still retains its original keychain namespace.
	c.Accounts[2].Qualification.IndependentLogin = false
	p := nativecontract.Policy{GrantID: "40000000-0000-4000-8000-000000000001", GrantRevision: 2, ExpiresAt: now.Add(time.Hour),
		ToolPolicyDigest: strings.Repeat("c", 64), SettingsPolicyDigest: strings.Repeat("d", 64), PermissionPolicy: "supervised",
		LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 300, MaximumTurns: 10, MaximumTokens: 10000}
	return c, f, p
}

func requestFor(e Enrollment, p nativecontract.Policy) nativecontract.ProfileRequest {
	return nativecontract.ProfileRequest{TenantID: e.TenantID, OwnerPersonID: e.OwnerPersonID, AccountID: e.AccountID,
		AccountKey: e.AccountKey, WorkspaceID: "project", Harness: e.Harness, BindingRevision: e.BindingRevision, Policy: p}
}
func environmentValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == name {
			return value, true
		}
	}
	return "", false
}

func TestProfileIsolationPreservesCredentialsAndParentEnvironment(t *testing.T) {
	c, files, policy := profileFixture()
	parent := append([]string(nil), c.ParentEnvironment...)
	credentials := map[string]string{}
	for key, value := range files.credentials {
		credentials[key] = value
	}
	// These are known test-only values. No real parent environment is printed.
	t.Setenv("CLAUDE_CONFIG_DIR", "parent-fixture-profile")
	t.Setenv("HOME", "parent-fixture-home")
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	a, b, defaultEnrollment := c.Accounts[0], c.Accounts[1], c.Accounts[2]
	// Later caller mutations cannot rotate a running process's account or env.
	c.Accounts[0].AccountHome = "/profiles/b"
	c.Accounts[0].Binary.Path = "/unapproved/claude"
	c.Workspaces[0].Root = "/other"
	c.ParentEnvironment[3] = "USER=other-fixture-user"
	files.ready, files.release = make(chan string, 2), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-files.release:
		default:
			close(files.release)
		}
	})
	type result struct {
		profile nativecontract.LocalProfile
		err     error
	}
	results := make(chan result, 2)
	for _, enrolled := range []Enrollment{a, b} {
		go func(e Enrollment) { p, err := r.Resolve(t.Context(), requestFor(e, policy)); results <- result{p, err} }(enrolled)
	}
	// Explicit metadata barriers prove the two independent children overlap.
	seen := map[string]bool{}
	for range 2 {
		select {
		case path := <-files.ready:
			seen[path] = true
		case <-time.After(5 * time.Second):
			t.Fatal("profile barrier hung")
		}
	}
	if !seen["/profiles/a"] || !seen["/profiles/b"] {
		t.Fatal("profiles did not overlap at distinct folders")
	}
	close(files.release)
	children := make(map[string]nativecontract.LocalProfile)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			children[result.profile.AccountID] = result.profile
		case <-time.After(5 * time.Second):
			t.Fatal("profile resolution hung")
		}
	}
	defaultProfile, err := r.Resolve(t.Context(), requestFor(defaultEnrollment, policy))
	if err != nil {
		t.Fatal(err)
	}
	children[accountDefault] = defaultProfile
	for _, e := range []Enrollment{a, b, defaultEnrollment} {
		p := children[e.AccountID]
		if p.AccountHome != e.AccountHome || p.ProfileBindingRevision != e.BindingRevision ||
			p.ConversationRoot != e.AccountHome || p.Binary != e.Binary || p.WorkspaceRoot != "/work/project" {
			t.Fatal("profile changed immutable binding")
		}
		want := []string{"HOME=/owner"}
		if e.AccountID != accountDefault {
			want = append(want, "CLAUDE_CONFIG_DIR="+e.AccountHome)
		}
		want = append(want, "USER=fixture-user", "LOGNAME=fixture-user", "TMPDIR=/scratch", "PATH=/vendor:/usr/bin:/bin", "LANG=C", "LC_ALL=C")
		if !reflect.DeepEqual(p.Environment, want) {
			t.Fatal("child environment violated its exact allowlist")
		}
		if _, err := json.Marshal(p); err == nil {
			t.Fatal("local profile became a public projection")
		}
	}
	if _, present := environmentValue(defaultProfile.Environment, "CLAUDE_CONFIG_DIR"); present {
		t.Fatal("default Claude keychain namespace changed")
	}
	children[accountA].Environment[0] = "HOME=/mutated"
	fresh, err := r.Resolve(t.Context(), requestFor(a, policy))
	if err != nil {
		t.Fatal(err)
	}
	if home, _ := environmentValue(fresh.Environment, "HOME"); home != "/owner" {
		t.Fatal("child environment aliases resolver state")
	}
	if home, _ := environmentValue(children[accountB].Environment, "HOME"); home != "/owner" {
		t.Fatal("overlapping children share mutable environment")
	}
	if os.Getenv("CLAUDE_CONFIG_DIR") != "parent-fixture-profile" || os.Getenv("HOME") != "parent-fixture-home" {
		t.Fatal("parent process environment changed")
	}
	if !reflect.DeepEqual(files.credentials, credentials) || len(files.forbidden) != 0 {
		t.Fatal("credential state was read, copied, or linked")
	}
	for _, path := range files.metadata {
		if _, forbidden := files.credentials[path]; forbidden {
			t.Fatal("credential metadata was inspected")
		}
	}
	// Only our intentional mutation changed the supplied parent snapshot.
	c.ParentEnvironment[3] = parent[3]
	if !reflect.DeepEqual(c.ParentEnvironment, parent) {
		t.Fatal("caller environment snapshot mutated")
	}
}

func TestProfileResolutionRejectsForeignStaleAndUnqualifiedAccounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config, *nativecontract.ProfileRequest)
		want   error
	}{
		{"foreign tenant", func(c *Config, q *nativecontract.ProfileRequest) { q.TenantID = ownerID }, ErrNotEnrolled},
		{"foreign owner", func(c *Config, q *nativecontract.ProfileRequest) { q.OwnerPersonID = tenantID }, ErrNotEnrolled},
		{"foreign trust context", func(c *Config, q *nativecontract.ProfileRequest) { c.Workspaces[0].TrustContext = "other-trust" }, ErrNotEnrolled},
		{"unknown enrollment", func(c *Config, q *nativecontract.ProfileRequest) { q.AccountID = ownerID }, ErrNotEnrolled},
		{"silent rotation", func(c *Config, q *nativecontract.ProfileRequest) { q.AccountKey = "b" }, ErrNotEnrolled},
		{"wrong provider", func(c *Config, q *nativecontract.ProfileRequest) { q.Harness = "codex" }, ErrNotEnrolled},
		{"stale revision", func(c *Config, q *nativecontract.ProfileRequest) { q.BindingRevision-- }, ErrBinding},
		{"unqualified native", func(c *Config, q *nativecontract.ProfileRequest) { c.Accounts[0].Qualification.Native = false }, ErrQualification},
		{"unqualified HOME", func(c *Config, q *nativecontract.ProfileRequest) {
			c.Accounts[0].Qualification.PreserveUserHome = false
		}, ErrQualification},
		{"unqualified login isolation", func(c *Config, q *nativecontract.ProfileRequest) {
			c.Accounts[0].Qualification.IndependentLogin = false
		}, ErrQualification},
		{"wrong OS", func(c *Config, q *nativecontract.ProfileRequest) { c.Accounts[0].Qualification.OS = "unqualified-os" }, ErrQualification},
		{"wrong pin", func(c *Config, q *nativecontract.ProfileRequest) {
			c.Accounts[0].Qualification.BinarySHA256 = strings.Repeat("e", 64)
		}, ErrQualification},
		{"wrong SDK pin", func(c *Config, q *nativecontract.ProfileRequest) {
			c.Accounts[0].Qualification.SDKSHA256 = strings.Repeat("e", 64)
		}, ErrQualification},
		{"unqualified history", func(c *Config, q *nativecontract.ProfileRequest) { c.Accounts[0].Qualification.VendorHistory = false }, ErrQualification},
		{"expired policy", func(c *Config, q *nativecontract.ProfileRequest) { q.Policy.ExpiresAt = c.Now() }, ErrPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, p := profileFixture()
			q := requestFor(c.Accounts[0], p)
			tc.change(&c, &q)
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := r.Resolve(t.Context(), q)
			if !errors.Is(err, tc.want) || profile.AccountID != "" || len(profile.Environment) != 0 {
				t.Fatal("wrong refusal or usable rejected profile")
			}
		})
	}
	c, _, policy := profileFixture()
	now := c.Now()
	c.Now = func() time.Time { return now }
	c.Accounts[0].Qualification.VendorHistory = false
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	policy.LocalHistory = "disabled"
	profile, err := r.Resolve(t.Context(), requestFor(c.Accounts[0], policy))
	if err != nil || profile.ConversationRoot != "" {
		t.Fatal("disabled history advertised persistence")
	}
	now = policy.ExpiresAt
	if _, err := r.Resolve(t.Context(), requestFor(c.Accounts[0], policy)); !errors.Is(err, ErrPolicy) {
		t.Fatal("expired policy remained usable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Resolve(ctx, requestFor(c.Accounts[0], policy)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not preserved")
	}
	t.Run("policy expires during metadata inspection", func(t *testing.T) {
		c, files, policy := profileFixture()
		now := c.Now()
		c.Now = func() time.Time { return now }
		r, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		files.ready, files.release = make(chan string, 1), make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-files.release:
			default:
				close(files.release)
			}
		})
		result := make(chan error, 1)
		go func() {
			p, err := r.Resolve(t.Context(), requestFor(c.Accounts[0], policy))
			if p.AccountID != "" {
				result <- errors.New("expired profile became launchable")
				return
			}
			result <- err
		}()
		select {
		case <-files.ready:
		case <-time.After(5 * time.Second):
			t.Fatal("expiry barrier hung")
		}
		now = policy.ExpiresAt
		close(files.release)
		select {
		case err := <-result:
			if !errors.Is(err, ErrPolicy) {
				t.Fatal("policy was not rechecked after inspection")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("expiry resolution hung")
		}
	})
}

func TestProfileDirectoriesRejectAliasesAndUnsafeHomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config, *fakeFilesystem)
		want   error
	}{
		{"shared enrollment home", func(c *Config, f *fakeFilesystem) { c.Accounts[1].AccountHome = c.Accounts[0].AccountHome }, ErrOverlap},
		{"nested history", func(c *Config, f *fakeFilesystem) {
			c.Accounts[1].AccountHome = "/profiles/a/nested"
			f.directories["/profiles/a/nested"] = Directory{fs.ModeDir | 0700, true, "nested"}
		}, ErrOverlap},
		{"aliased inode", func(c *Config, f *fakeFilesystem) {
			d := f.directories["/profiles/b"]
			d.Identity = "/profiles/a"
			f.directories["/profiles/b"] = d
		}, ErrOverlap},
		{"symlinked parent", func(c *Config, f *fakeFilesystem) {
			f.directories["/profiles"] = Directory{fs.ModeSymlink | 0777, true, "link"}
		}, ErrDirectory},
		{"symlinked profile", func(c *Config, f *fakeFilesystem) {
			f.directories["/profiles/a"] = Directory{fs.ModeSymlink | 0777, true, "link"}
		}, ErrDirectory},
		{"nonprivate profile", func(c *Config, f *fakeFilesystem) {
			f.directories["/profiles/a"] = Directory{fs.ModeDir | 0755, true, "a"}
		}, ErrDirectory},
		{"foreign OS owner", func(c *Config, f *fakeFilesystem) {
			f.directories["/profiles/a"] = Directory{fs.ModeDir | 0700, false, "a"}
		}, ErrDirectory},
		{"missing profile", func(c *Config, f *fakeFilesystem) { delete(f.directories, "/profiles/a") }, ErrDirectory},
		{"profile is HOME", func(c *Config, f *fakeFilesystem) { c.Accounts[0].AccountHome = c.UserHome }, ErrOverlap},
		{"workspace contains login state", func(c *Config, f *fakeFilesystem) { c.Workspaces[0].Root = "/profiles" }, ErrOverlap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, files, _ := profileFixture()
			tc.change(&c, files)
			if _, err := New(c); !errors.Is(err, tc.want) {
				t.Fatal("unsafe profile failed for the wrong reason")
			}
			if len(files.forbidden) != 0 {
				t.Fatal("unsafe profile caused credential IO")
			}
		})
	}
	t.Run("replacement after enrollment", func(t *testing.T) {
		c, files, policy := profileFixture()
		r, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		dir := files.directories["/profiles/a"]
		dir.Identity = "replacement"
		files.directories["/profiles/a"] = dir
		if _, err := r.Resolve(t.Context(), requestFor(c.Accounts[0], policy)); !errors.Is(err, ErrBinding) {
			t.Fatal("replaced profile retained its binding")
		}
	})
	t.Run("real private metadata", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dir, err := inspect(localFilesystem{}, root, true)
		if err != nil || !dir.Owned || dir.Identity == "" {
			t.Fatal("private OS directory was not recognized")
		}
		if err := os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := inspect(localFilesystem{}, root, true); !errors.Is(err, ErrDirectory) {
			t.Fatal("public OS profile remained usable")
		}
	})
}

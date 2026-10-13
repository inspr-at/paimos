// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeprofiles resolves already enrolled, operator-qualified local
// accounts. It never logs in, rotates accounts, or opens vendor credential files.
package nativeprofiles

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

const (
	maxEntries  = 256
	maxPath     = 4096
	maxRevision = 1<<53 - 1
)

var (
	ErrInvalid       = errors.New("invalid native profile configuration")
	ErrNotEnrolled   = errors.New("native account is not enrolled in this owner context")
	ErrBinding       = errors.New("native account binding changed")
	ErrQualification = errors.New("native account profile is not qualified")
	ErrDirectory     = errors.New("native profile directory is not physical, private and owner-controlled")
	ErrOverlap       = errors.New("native account profiles must not share or overlap directories")
	ErrPolicy        = errors.New("native profile policy is invalid or expired")
)

// Scope comes from trusted enrollment/workspace state, never public path input.
// TrustContext distinguishes contexts even within one tenant and person.
type Scope struct {
	TenantID, OwnerPersonID, TrustContext string
}

// Qualification is operator-owned evidence for this exact OS, pin and revision.
// A folder alone never establishes IndependentLogin (notably on macOS).
type Qualification struct {
	OS, BinarySHA256, SDKSHA256                               string
	Native, PreserveUserHome, IndependentLogin, VendorHistory bool
}

// Enrollment is a snapshot of an EXISTING enrollment and its approved local
// directory. Several legacy enrollments in one rotating home cannot be installed
// in this resolver. Official login/enrollment remains person/OPS-led.
type Enrollment struct {
	Scope
	AccountID, AccountKey, Harness, AccountHome string
	BindingRevision                             int64
	Binary, SDK                                 nativecontract.BinaryPin
	Qualification                               Qualification
}

type Workspace struct {
	Scope
	ID, Root string
}

// Config and enrollment paths are local-only. ParentEnvironment is a supplied
// snapshot; only USER, LOGNAME and TMPDIR survive into a child environment.
type Config struct {
	UserHome          string
	Accounts          []Enrollment
	Workspaces        []Workspace
	ParentEnvironment []string
	Filesystem        Filesystem
	Now               func() time.Time
}

type account struct {
	enrollment Enrollment
	directory  Directory
}

// Resolver has no rebind/update method. Each process receives a value snapshot;
// an authorized stopped successor needs fresh admission and a fresh resolver.
type Resolver struct {
	userHome             string
	userDirectory        Directory
	accounts             map[string]account
	workspaces           map[string]Workspace
	workspaceDirectories map[string]Directory
	loginEnvironment     []string
	filesystem           Filesystem
	now                  func() time.Time
}

var _ nativecontract.ProfileResolver = (*Resolver)(nil)

// New copies trusted state and inspects directory metadata only. It deliberately
// refuses missing folders: the operator creates and signs into dedicated profiles
// with the unmodified official vendor flow before enrollment/qualification.
func New(c Config) (*Resolver, error) {
	if len(c.Accounts) == 0 || len(c.Accounts) > maxEntries || len(c.Workspaces) == 0 || len(c.Workspaces) > maxEntries {
		return nil, ErrInvalid
	}
	login, err := loginEnvironment(c.ParentEnvironment)
	if err != nil {
		return nil, err
	}
	if c.Filesystem == nil {
		c.Filesystem = localFilesystem{}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	r := &Resolver{userHome: c.UserHome, filesystem: c.Filesystem, now: c.Now,
		accounts: make(map[string]account), workspaces: make(map[string]Workspace),
		workspaceDirectories: make(map[string]Directory), loginEnvironment: login}
	r.userDirectory, err = inspect(r.filesystem, r.userHome, false)
	if err != nil {
		return nil, err
	}
	for _, a := range c.Accounts {
		if !validScope(a.Scope) || !validID(a.AccountID) || !validText(a.AccountKey, 128) ||
			a.BindingRevision < 0 || a.BindingRevision > maxRevision || !validPin(a.Binary) {
			return nil, ErrInvalid
		}
		if _, ok := r.accounts[a.AccountID]; ok {
			return nil, ErrInvalid
		}
		if _, ok := variable(a.Harness); !ok {
			return nil, ErrQualification
		}
		if a.Harness == "claude" && !validPin(a.SDK) {
			return nil, ErrInvalid
		}
		if a.SDK != (nativecontract.BinaryPin{}) && !validPin(a.SDK) {
			return nil, ErrInvalid
		}
		dir, err := inspect(r.filesystem, a.AccountHome, true)
		if err != nil {
			return nil, err
		}
		for _, existing := range r.accounts {
			if overlaps(a.AccountHome, existing.enrollment.AccountHome) || dir.Identity == existing.directory.Identity {
				return nil, ErrOverlap
			}
			if a.AccountKey == existing.enrollment.AccountKey {
				return nil, ErrInvalid
			}
		}
		// Do not let a profile become HOME itself or absorb another vendor's
		// default history/config. Dedicated profiles may live below real HOME.
		if within(a.AccountHome, r.userHome) {
			return nil, ErrOverlap
		}
		r.accounts[a.AccountID] = account{a, dir}
	}
	for _, w := range c.Workspaces {
		if !validScope(w.Scope) || !validText(w.ID, 128) {
			return nil, ErrInvalid
		}
		if _, ok := r.workspaces[w.ID]; ok {
			return nil, ErrInvalid
		}
		dir, err := inspect(r.filesystem, w.Root, false)
		if err != nil {
			return nil, err
		}
		for _, a := range r.accounts {
			if overlaps(w.Root, a.enrollment.AccountHome) {
				return nil, ErrOverlap
			}
		}
		r.workspaces[w.ID], r.workspaceDirectories[w.ID] = w, dir
	}
	return r, nil
}

func (r *Resolver) Resolve(ctx context.Context, q nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	var empty nativecontract.LocalProfile
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !validID(q.TenantID) || !validID(q.OwnerPersonID) || !validID(q.AccountID) ||
		!validText(q.AccountKey, 128) || !validText(q.WorkspaceID, 128) || !validText(q.Harness, 128) ||
		q.BindingRevision < 0 || q.BindingRevision > maxRevision {
		return empty, ErrInvalid
	}
	a, ok := r.accounts[q.AccountID]
	if !ok || a.enrollment.AccountKey != q.AccountKey || a.enrollment.Harness != q.Harness ||
		a.enrollment.TenantID != q.TenantID || a.enrollment.OwnerPersonID != q.OwnerPersonID {
		return empty, ErrNotEnrolled
	}
	e := a.enrollment
	if q.BindingRevision != e.BindingRevision {
		return empty, ErrBinding
	}
	w, ok := r.workspaces[q.WorkspaceID]
	if !ok || w.Scope != e.Scope {
		return empty, ErrNotEnrolled
	}
	if !validPolicy(q.Policy, r.now()) {
		return empty, ErrPolicy
	}
	qual := e.Qualification
	defaultClaude := e.Harness == "claude" && e.AccountHome == filepath.Join(r.userHome, ".claude")
	if !qual.Native || !qual.PreserveUserHome || qual.OS != runtime.GOOS ||
		qual.BinarySHA256 != e.Binary.SHA256 || qual.SDKSHA256 != e.SDK.SHA256 ||
		!defaultClaude && !qual.IndependentLogin || q.Policy.LocalHistory == "vendor_native" && !qual.VendorHistory {
		return empty, ErrQualification
	}
	// Metadata is rechecked immediately before handing a profile to a launcher.
	// P4c/I2 still own current identity, grant and occupancy revalidation at spawn.
	for _, check := range []struct {
		path     string
		private  bool
		original Directory
	}{
		{r.userHome, false, r.userDirectory}, {e.AccountHome, true, a.directory},
		{w.Root, false, r.workspaceDirectories[w.ID]},
	} {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		dir, err := inspect(r.filesystem, check.path, check.private)
		if err != nil {
			return empty, err
		}
		if dir.Identity != check.original.Identity {
			return empty, ErrBinding
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !validPolicy(q.Policy, r.now()) {
		return empty, ErrPolicy
	}
	environment := []string{"HOME=" + r.userHome}
	if !defaultClaude {
		name, _ := variable(e.Harness)
		environment = append(environment, name+"="+e.AccountHome)
	}
	environment = append(environment, r.loginEnvironment...)
	path := []string{filepath.Dir(e.Binary.Path), "/usr/bin", "/bin"}
	environment = append(environment, "PATH="+strings.Join(path, string(filepath.ListSeparator)), "LANG=C", "LC_ALL=C")
	conversationRoot := ""
	if q.Policy.LocalHistory == "vendor_native" {
		conversationRoot = e.AccountHome
	}
	return nativecontract.LocalProfile{AccountID: e.AccountID, AccountHome: e.AccountHome,
		UserHome: r.userHome, WorkspaceRoot: w.Root, ConversationRoot: conversationRoot,
		Environment: environment, Binary: e.Binary, SDK: e.SDK, ProfileBindingRevision: e.BindingRevision}, nil
}

func variable(harness string) (string, bool) {
	switch harness {
	case "claude":
		return "CLAUDE_CONFIG_DIR", true
	case "codex":
		return "CODEX_HOME", true
	case "pi":
		return "PI_CODING_AGENT_DIR", true
	case "cursor":
		return "CURSOR_CONFIG_DIR", true
	case "grok":
		return "GROK_HOME", true
	default:
		return "", false // ACP profiles needing HOME await separate qualification.
	}
}

func loginEnvironment(parent []string) ([]string, error) {
	if len(parent) > 512 {
		return nil, ErrInvalid
	}
	values := make(map[string]string)
	bytes := 0
	for _, entry := range parent {
		bytes += len(entry)
		if len(entry) > 8192 || bytes > 65536 {
			return nil, ErrInvalid
		}
		name, value, ok := strings.Cut(entry, "=")
		if name != "USER" && name != "LOGNAME" && name != "TMPDIR" {
			continue
		}
		if !ok || !validText(value, maxPath) {
			return nil, ErrInvalid
		}
		if _, exists := values[name]; exists {
			return nil, ErrInvalid
		}
		values[name] = value
	}
	child := make([]string, 0, 3)
	for _, name := range []string{"USER", "LOGNAME", "TMPDIR"} {
		if v := values[name]; v != "" {
			child = append(child, name+"="+v)
		}
	}
	return child, nil
}

func validScope(s Scope) bool {
	return validID(s.TenantID) && validID(s.OwnerPersonID) && validText(s.TrustContext, 128)
}
func validID(s string) bool {
	id, err := uuid.Parse(s)
	return len(s) == 36 && err == nil && id.String() == s && id != uuid.Nil
}
func validText(s string, limit int) bool {
	return len(s) > 0 && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func validPath(s string) bool {
	return validText(s, maxPath) && filepath.IsAbs(s) && filepath.Clean(s) == s
}
func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validPin(p nativecontract.BinaryPin) bool {
	return validPath(p.Path) && !strings.ContainsRune(p.Path, filepath.ListSeparator) && digest(p.SHA256) && validText(p.Version, 128) && validText(p.Protocol, 128)
}
func validPolicy(p nativecontract.Policy, now time.Time) bool {
	return validID(p.GrantID) && p.GrantRevision >= 0 && p.GrantRevision <= maxRevision && p.ExpiresAt.After(now) &&
		digest(p.ToolPolicyDigest) && digest(p.SettingsPolicyDigest) &&
		(p.PermissionPolicy == "supervised" || p.PermissionPolicy == "bounded_preapproved") &&
		(p.LocalHistory == "disabled" || p.LocalHistory == "vendor_native") &&
		(p.Continuation == "manual" || p.Continuation == "bounded_preapproved") &&
		p.MaximumRuntimeSeconds >= 1 && p.MaximumRuntimeSeconds <= 86400 &&
		p.MaximumTurns >= 1 && p.MaximumTurns <= 100000 && p.MaximumTokens >= 1 && p.MaximumTokens <= 100000000
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func overlaps(a, b string) bool { return within(a, b) || within(b, a) }

// Enrollment/configuration are not wire DTOs, like C0's LocalProfile.
func (Enrollment) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (Workspace) MarshalJSON() ([]byte, error)  { return nil, ErrInvalid }
func (Config) MarshalJSON() ([]byte, error)     { return nil, ErrInvalid }

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var accountKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const maxCodexAccountRegistryBytes = 64 << 10

var inheritedCodexEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TMP", "TEMP",
	"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TZ",
	"XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE",
}

// CodexAccountRegistry is the operator-local mapping from opaque non-secret
// keys to private Codex homes and expected ChatGPT emails. Homes and emails
// never enter public start requests or receipts.
type CodexAccountRegistry struct {
	byKey map[string]codexAccount
}

type codexAccount struct {
	home  string
	email string
}

type codexAccountRegistryFile struct {
	Accounts []codexAccountRegistryEntry `json:"accounts"`
}

type codexAccountRegistryEntry struct {
	Key   string `json:"key"`
	Home  string `json:"home"`
	Email string `json:"email"`
}

type accountContextResolver interface {
	HasAccount(string) bool
}

type accountSelection interface {
	AccountSelection() (string, string)
}

func ValidAccountKey(value string) bool { return validAccountKey(value) }

func validAccountKey(value string) bool {
	if !accountKeyPattern.MatchString(value) || strings.ContainsAny(value, "/\\") {
		return false
	}
	if IsClosedAccountLabel(value) || value == "local_probe" {
		return false
	}
	return true
}

// IsClosedAccountLabel reports the existing non-secret class label, which is
// independent of an opaque named-account key.
func IsClosedAccountLabel(value string) bool {
	return validAccountLabel(value) && value != "unknown"
}

func validExpectedEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 254 || strings.ContainsAny(value, " \t\x00\r\n") {
		return false
	}
	at := strings.IndexByte(value, '@')
	return at > 0 && at == strings.LastIndexByte(value, '@') && at < len(value)-1
}

// ParseCodexAccountRegistry loads an operator-controlled registry. Public APIs
// never receive these homes or emails; callers pass only the opaque key.
func ParseCodexAccountRegistry(raw []byte) (CodexAccountRegistry, error) {
	if len(raw) == 0 || len(raw) > maxCodexAccountRegistryBytes {
		return CodexAccountRegistry{}, errors.New("codex account registry is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file codexAccountRegistryFile
	if decoder.Decode(&file) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(file.Accounts) == 0 || len(file.Accounts) > 16 {
		return CodexAccountRegistry{}, errors.New("codex account registry is invalid")
	}
	out := CodexAccountRegistry{byKey: map[string]codexAccount{}}
	homes := map[string]string{}
	emails := map[string]string{}
	for _, entry := range file.Accounts {
		key := strings.TrimSpace(entry.Key)
		if !validAccountKey(key) || out.byKey[key].home != "" {
			return CodexAccountRegistry{}, errors.New("codex account registry key is invalid")
		}
		home, err := canonicalCodexHome(entry.Home)
		if err != nil {
			return CodexAccountRegistry{}, err
		}
		email := strings.TrimSpace(entry.Email)
		if !validExpectedEmail(email) {
			return CodexAccountRegistry{}, errors.New("codex account registry identity is invalid")
		}
		folded := strings.ToLower(email)
		if homes[home] != "" || emails[folded] != "" {
			return CodexAccountRegistry{}, errors.New("codex account registry is invalid")
		}
		homes[home] = key
		emails[folded] = key
		out.byKey[key] = codexAccount{home: home, email: email}
	}
	return out, nil
}

func canonicalCodexHome(value string) (string, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("codex account home is invalid")
	}
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical || strings.ContainsAny(canonical, "\x00\r\n") {
		return "", errors.New("codex account home cannot be pinned")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("codex account home is not a directory")
	}
	return canonical, nil
}

func (r CodexAccountRegistry) lookup(key string) (codexAccount, bool) {
	account, ok := r.byKey[key]
	return account, ok
}

func (r CodexAccountRegistry) HasAccount(key string) bool {
	_, ok := r.lookup(key)
	return ok
}

func inheritOperatorEnv() []string {
	var out []string
	for _, name := range inheritedCodexEnvNames {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

func applyCodexHome(env []string, home string) []string {
	source := env
	if source == nil {
		source = inheritOperatorEnv()
	}
	out := make([]string, 0, len(source)+1)
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "CODEX_HOME" {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "CODEX_HOME="+home)
}

const maxPiAccountRegistryBytes = 64 << 10

// PiAccountRegistry maps opaque non-secret keys to protected Pi agent
// directories. Directories never enter public start requests or receipts.
type PiAccountRegistry struct {
	byKey map[string]piAccount
}

type piAccount struct {
	agentDir string
}

type piAccountRegistryFile struct {
	Accounts []piAccountRegistryEntry `json:"accounts"`
}

type piAccountRegistryEntry struct {
	Key      string `json:"key"`
	AgentDir string `json:"agent_dir"`
}

// ParsePiAccountRegistry loads an operator-controlled Pi context registry.
// Public APIs never receive these directories; callers pass only the opaque key.
func ParsePiAccountRegistry(raw []byte) (PiAccountRegistry, error) {
	if len(raw) == 0 || len(raw) > maxPiAccountRegistryBytes {
		return PiAccountRegistry{}, errors.New("pi account registry is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file piAccountRegistryFile
	if decoder.Decode(&file) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(file.Accounts) == 0 || len(file.Accounts) > 16 {
		return PiAccountRegistry{}, errors.New("pi account registry is invalid")
	}
	out := PiAccountRegistry{byKey: map[string]piAccount{}}
	dirs := map[string]string{}
	for _, entry := range file.Accounts {
		key := strings.TrimSpace(entry.Key)
		if !validAccountKey(key) || out.byKey[key].agentDir != "" {
			return PiAccountRegistry{}, errors.New("pi account registry key is invalid")
		}
		dir, err := canonicalPiAgentDir(entry.AgentDir)
		if err != nil {
			return PiAccountRegistry{}, err
		}
		if dirs[dir] != "" {
			return PiAccountRegistry{}, errors.New("pi account registry is invalid")
		}
		dirs[dir] = key
		out.byKey[key] = piAccount{agentDir: dir}
	}
	return out, nil
}

func canonicalPiAgentDir(value string) (string, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("pi account directory is invalid")
	}
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical || strings.ContainsAny(canonical, "\x00\r\n") {
		return "", errors.New("pi account directory cannot be pinned")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("pi account directory is not a directory")
	}
	return canonical, nil
}

func (r PiAccountRegistry) lookup(key string) (piAccount, bool) {
	account, ok := r.byKey[key]
	return account, ok
}

func (r PiAccountRegistry) HasAccount(key string) bool {
	_, ok := r.lookup(key)
	return ok
}

const maxCursorAccountRegistryBytes = 64 << 10

// CursorAccountRegistry maps opaque non-secret keys to expected Cursor
// identity. Emails and user ids stay in the operator-local registry; they
// never enter public start requests, profiles, or receipts. Tokens remain in
// the vendor store: this mapping does not copy auth or set HOME.
type CursorAccountRegistry struct {
	byKey map[string]cursorAccount
}

type cursorAccount struct {
	email  string
	userID string
}

type cursorAccountRegistryFile struct {
	Accounts []cursorAccountRegistryEntry `json:"accounts"`
}

type cursorAccountRegistryEntry struct {
	Key    string `json:"key"`
	Email  string `json:"email"`
	UserID string `json:"user_id,omitempty"`
}

// ParseCursorAccountRegistry loads an operator-controlled Cursor identity
// registry. Public APIs never receive these emails; callers pass only the
// opaque key.
func ParseCursorAccountRegistry(raw []byte) (CursorAccountRegistry, error) {
	if len(raw) == 0 || len(raw) > maxCursorAccountRegistryBytes {
		return CursorAccountRegistry{}, errors.New("cursor account registry is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file cursorAccountRegistryFile
	if decoder.Decode(&file) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(file.Accounts) == 0 || len(file.Accounts) > 16 {
		return CursorAccountRegistry{}, errors.New("cursor account registry is invalid")
	}
	out := CursorAccountRegistry{byKey: map[string]cursorAccount{}}
	emails := map[string]string{}
	userIDs := map[string]string{}
	for _, entry := range file.Accounts {
		key := strings.TrimSpace(entry.Key)
		if !validAccountKey(key) || out.byKey[key].email != "" {
			return CursorAccountRegistry{}, errors.New("cursor account registry key is invalid")
		}
		email := strings.TrimSpace(entry.Email)
		if !validExpectedEmail(email) {
			return CursorAccountRegistry{}, errors.New("cursor account registry identity is invalid")
		}
		userID := strings.TrimSpace(entry.UserID)
		if userID != "" && !validCursorUserID(userID) {
			return CursorAccountRegistry{}, errors.New("cursor account registry identity is invalid")
		}
		folded := strings.ToLower(email)
		if emails[folded] != "" {
			return CursorAccountRegistry{}, errors.New("cursor account registry is invalid")
		}
		if userID != "" && userIDs[userID] != "" {
			return CursorAccountRegistry{}, errors.New("cursor account registry is invalid")
		}
		emails[folded] = key
		if userID != "" {
			userIDs[userID] = key
		}
		out.byKey[key] = cursorAccount{email: email, userID: userID}
	}
	return out, nil
}

func validCursorUserID(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, " \t\x00\r\n") {
		return false
	}
	return true
}

func (r CursorAccountRegistry) lookup(key string) (cursorAccount, bool) {
	account, ok := r.byKey[key]
	return account, ok
}

func (r CursorAccountRegistry) HasAccount(key string) bool {
	_, ok := r.lookup(key)
	return ok
}

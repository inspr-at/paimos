// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package publicbase owns the optional native public URL prefix.
//
// Empty means today's standalone origin-root. A non-empty value is a
// canonical ASCII absolute path used as an exact segment-boundary mount.
// The configured value is the only source of truth: request headers such
// as X-Forwarded-Prefix are never consulted.
package publicbase

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"
)

// EnvName is the operator-owned configuration variable.
const EnvName = "PAIMOS_PUBLIC_BASE_PATH"

// Path is a validated public base path. The zero value is standalone root.
type Path string

var (
	currentMu sync.RWMutex
	current   Path
)

var segmentAlphabet = func() [256]bool {
	var ok [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		ok[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		ok[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		ok[c] = true
	}
	ok['_'] = true
	ok['-'] = true
	return ok
}()

// Parse validates a configured public base path.
//
// Allowed values are empty or one-or-more `/` + `[A-Za-z0-9_-]+` segments.
// Trailing slashes, empty or dot segments, percent-encoding, backslash,
// query, fragment, control bytes, and protocol-relative paths are rejected.
func Parse(raw string) (Path, error) {
	if raw == "" {
		return "", nil
	}
	if strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("%s: surrounding whitespace is not allowed", EnvName)
	}
	if !validASCIIPathBytes(raw) {
		return "", fmt.Errorf("%s: must be a canonical ASCII absolute path", EnvName)
	}
	if strings.HasPrefix(raw, "//") {
		return "", fmt.Errorf("%s: protocol-relative paths are not allowed", EnvName)
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%s: must be an absolute path starting with /", EnvName)
	}
	if strings.HasSuffix(raw, "/") {
		return "", fmt.Errorf("%s: trailing slash is not allowed", EnvName)
	}
	if strings.ContainsAny(raw, `\%?#`) {
		return "", fmt.Errorf("%s: percent-encoding, backslash, query, and fragment are not allowed", EnvName)
	}
	segments := strings.Split(raw[1:], "/")
	if len(segments) == 0 {
		return "", fmt.Errorf("%s: empty path is written as an empty string, not /", EnvName)
	}
	for _, segment := range segments {
		if segment == "" {
			return "", fmt.Errorf("%s: empty path segments are not allowed", EnvName)
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("%s: dot segments are not allowed", EnvName)
		}
		for i := 0; i < len(segment); i++ {
			if !segmentAlphabet[segment[i]] {
				return "", fmt.Errorf("%s: each segment must match [A-Za-z0-9_-]+", EnvName)
			}
		}
	}
	return Path(raw), nil
}

func validASCIIPathBytes(raw string) bool {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c > unicode.MaxASCII || unicode.IsControl(rune(c)) {
			return false
		}
	}
	return true
}

// LoadFromEnv reads and validates PAIMOS_PUBLIC_BASE_PATH. Missing or empty
// is standalone root.
func LoadFromEnv() (Path, error) {
	return Parse(os.Getenv(EnvName))
}

// SetCurrent installs the process-wide prefix. Tests restore "" via Cleanup.
func SetCurrent(p Path) {
	currentMu.Lock()
	current = p
	currentMu.Unlock()
}

// Current returns the process-wide configured prefix.
func Current() Path {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}

// String returns the canonical prefix, or "".
func (p Path) String() string { return string(p) }

// Empty reports standalone root.
func (p Path) Empty() bool { return p == "" }

// SharedOrigin reports a configured native prefix (common-origin mount).
func (p Path) SharedOrigin() bool { return p != "" }

// Join prefixes an app-rooted path. appPath must be a same-origin absolute
// path; protocol-relative input is treated as `/`. A path that already carries
// this prefix is not doubled.
func (p Path) Join(appPath string) string {
	if appPath == "" {
		appPath = "/"
	}
	if strings.HasPrefix(appPath, "//") {
		appPath = "/"
	}
	if !strings.HasPrefix(appPath, "/") {
		appPath = "/" + appPath
	}
	if p == "" {
		return appPath
	}
	if stripped, ok := p.Strip(appPath); ok {
		appPath = stripped
	}
	if appPath == "/" {
		return string(p)
	}
	return string(p) + appPath
}

// Strip removes the configured prefix at an exact segment boundary.
// Empty prefix is a no-op that always matches.
func (p Path) Strip(requestPath string) (string, bool) {
	if p == "" {
		if requestPath == "" {
			return "/", true
		}
		return requestPath, true
	}
	prefix := string(p)
	if requestPath == prefix {
		return "/", true
	}
	if strings.HasPrefix(requestPath, prefix+"/") {
		rest := requestPath[len(prefix):]
		if rest == "" {
			return "/", true
		}
		return rest, true
	}
	return "", false
}

// AppPath returns the app-relative path used by routing and PAI-809
// classification: stripped when the request sits on this mount, otherwise
// the original path so a leftover root-shaped control URL stays private.
func (p Path) AppPath(requestPath string) string {
	if stripped, ok := p.Strip(requestPath); ok {
		return stripped
	}
	return requestPath
}

// BaseHref is the HTML <base href> for the one built SPA artifact.
func (p Path) BaseHref() string {
	if p == "" {
		return "/"
	}
	return string(p) + "/"
}

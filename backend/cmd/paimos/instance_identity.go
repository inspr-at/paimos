// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// instanceIdentity is the stable confidentiality boundary used by local
// knowledge caches. The human-readable instance name alone is insufficient:
// env-only targets are all named "env", and a configured name can be repointed.
type instanceIdentity struct {
	Name      string `json:"instance"`
	Origin    string `json:"origin"`
	Namespace string `json:"namespace"`
}

func newInstanceIdentity(name, rawURL string) (instanceIdentity, error) {
	origin, err := canonicalInstanceOrigin(rawURL)
	if err != nil {
		return instanceIdentity{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return instanceIdentity{}, fmt.Errorf("instance name is empty")
	}
	sum := sha256.Sum256([]byte(name + "\x00" + origin))
	return instanceIdentity{
		Name:      name,
		Origin:    origin,
		Namespace: safeInstanceLabel(name) + "-" + hex.EncodeToString(sum[:6]),
	}, nil
}

func canonicalInstanceOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid instance URL %q", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("instance URL must not contain userinfo")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func safeInstanceLabel(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-_")
	if label == "" {
		return "instance"
	}
	return label
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package publicbase

import (
	"strings"
	"testing"
)

func TestParseRejectsNonCanonical(t *testing.T) {
	invalid := []string{
		" ",
		" /paimos",
		"/paimos ",
		"paimos",
		"/",
		"/paimos/",
		"/paimos//app",
		"/./paimos",
		"/paimos/.",
		"/paimos/..",
		"/paimos/../x",
		"//paimos",
		"/paimos?",
		"/paimos?x=1",
		"/paimos#x",
		`/paimos\x`,
		"/paimos%2Fapp",
		"/paimos app",
		"/paimos/foo bar",
		"/päimos",
		"/\npaimos",
		"/paimos/",
		"/paimos.",
		"/paimos/foo.bar",
		"/paimos/foo:bar",
		"/paimos/foo@bar",
	}
	for _, raw := range invalid {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) accepted, want error", raw)
		}
	}
}

func TestParseAcceptsCanonical(t *testing.T) {
	cases := []string{"", "/paimos", "/Paimos", "/apps/paimos", "/a", "/A1_b-2"}
	for _, raw := range cases {
		got, err := Parse(raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", raw, err)
			continue
		}
		if got.String() != raw {
			t.Errorf("Parse(%q)=%q", raw, got)
		}
	}
}

func TestStripExactSegmentBoundary(t *testing.T) {
	prefix, err := Parse("/paimos")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"/paimos", "/", true},
		{"/paimos/", "/", true},
		{"/paimos/api/health", "/api/health", true},
		{"/paimos/api/control-commands/a%2Fb", "/api/control-commands/a%2Fb", true},
		{"/paimosfoo", "", false},
		{"/paimos2", "", false},
		{"/paimos-extra", "", false},
		{"/Paimos/api", "", false},
		{"/pai", "", false},
		{"/", "", false},
		{"/api/health", "", false},
		{"/pharos", "", false},
	}
	for _, tc := range cases {
		got, ok := prefix.Strip(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("Strip(%q)=(%q,%v) want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}

	empty := Path("")
	got, ok := empty.Strip("/api/health")
	if !ok || got != "/api/health" {
		t.Errorf("empty Strip = (%q,%v)", got, ok)
	}
}

func TestJoinDoesNotDoubleAndRejectsProtocolRelative(t *testing.T) {
	prefix, _ := Parse("/paimos")
	cases := []struct {
		prefix Path
		in     string
		want   string
	}{
		{"", "/api/health", "/api/health"},
		{"", "/", "/"},
		{prefix, "/api/health", "/paimos/api/health"},
		{prefix, "/", "/paimos"},
		{prefix, "/login", "/paimos/login"},
		{prefix, "/paimos/login", "/paimos/login"},
		{prefix, "/paimos", "/paimos"},
		{prefix, "//evil.example", "/paimos"},
		{prefix, "api/health", "/paimos/api/health"},
	}
	for _, tc := range cases {
		if got := tc.prefix.Join(tc.in); got != tc.want {
			t.Errorf("Join(%q,%q)=%q want %q", tc.prefix, tc.in, got, tc.want)
		}
	}
}

func TestAppPathKeepsUnstrippedControlShapedPaths(t *testing.T) {
	prefix, _ := Parse("/paimos")
	if got := prefix.AppPath("/paimos/api/runs/17/control-commands"); got != "/api/runs/17/control-commands" {
		t.Fatalf("prefixed app path = %q", got)
	}
	if got := prefix.AppPath("/api/runs/17/control-commands"); got != "/api/runs/17/control-commands" {
		t.Fatalf("root-shaped leftover = %q", got)
	}
	if got := prefix.AppPath("/paimos2/api/runs/17/control-commands"); got != "/paimos2/api/runs/17/control-commands" {
		t.Fatalf("near-miss mount = %q", got)
	}
}

func TestLoadFromEnvEmptyDefault(t *testing.T) {
	t.Setenv(EnvName, "")
	got, err := LoadFromEnv()
	if err != nil || !got.Empty() {
		t.Fatalf("empty default: got=%q err=%v", got, err)
	}
	t.Setenv(EnvName, "/paimos")
	got, err = LoadFromEnv()
	if err != nil || got.String() != "/paimos" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	t.Setenv(EnvName, "/paimos/")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("trailing slash must fail closed")
	}
}

func TestInjectIndexUsesConfiguredPrefixNotHeaders(t *testing.T) {
	src := []byte("<!doctype html><html><head><title>x</title></head></html>")
	got := string(InjectIndexBootstrap(src, Path("")))
	if !strings.Contains(got, `window.__PAIMOS_PUBLIC_BASE_PATH__=""`) {
		t.Fatalf("empty inject missing: %s", got)
	}
	if !strings.Contains(got, `<base href="/">`) {
		t.Fatalf("empty base missing: %s", got)
	}
	prefixed := string(InjectIndexBootstrap(src, Path("/paimos")))
	if !strings.Contains(prefixed, `window.__PAIMOS_PUBLIC_BASE_PATH__="/paimos"`) {
		t.Fatalf("prefix inject missing: %s", prefixed)
	}
	if !strings.Contains(prefixed, `<base href="/paimos/">`) {
		t.Fatalf("prefixed base missing: %s", prefixed)
	}
	if strings.Contains(prefixed, "X-Forwarded") {
		t.Fatal("inject must not mention forwarded headers")
	}
}

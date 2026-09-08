// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import "testing"

func TestJoinInstanceAPIOnce(t *testing.T) {
	cases := []struct {
		base string
		path string
		want string
	}{
		{"https://pm.example.com", "/api/health", "https://pm.example.com/api/health"},
		{"https://pm.example.com/", "/api/health", "https://pm.example.com/api/health"},
		{"https://pm.example.com/paimos", "/api/health", "https://pm.example.com/paimos/api/health"},
		{"https://pm.example.com/paimos/", "/api/health", "https://pm.example.com/paimos/api/health"},
		{"https://pm.example.com/apps/paimos", "/api/projects/7/agents", "https://pm.example.com/apps/paimos/api/projects/7/agents"},
	}
	for _, tc := range cases {
		got := joinInstanceAPI(tc.base, tc.path)
		if got != tc.want {
			t.Errorf("join(%q,%q)=%q want %q", tc.base, tc.path, got, tc.want)
		}
		if count := countAPISeg(got); count != 1 {
			t.Errorf("join(%q,%q) contains %d /api segments", tc.base, tc.path, count)
		}
	}
}

func countAPISeg(u string) int {
	n, from := 0, 0
	for {
		i := indexAPI(u[from:])
		if i < 0 {
			return n
		}
		n++
		from += i + 4
	}
}

func indexAPI(s string) int {
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == "/api" && (i+4 == len(s) || s[i+4] == '/' || s[i+4] == '?') {
			return i
		}
	}
	return -1
}

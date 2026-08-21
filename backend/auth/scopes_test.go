// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import "testing"

func TestParseScopesFailsClosedOnEmptyStorage(t *testing.T) {
	for _, stored := range []string{"", " ", "\t", ",", " , , "} {
		t.Run(stored, func(t *testing.T) {
			scopes := ParseScopes(stored)
			if len(scopes) != 0 || scopes.Has(ScopeAll) || scopes.Has("projects:write") {
				t.Fatalf("ParseScopes(%q) granted authority: %#v", stored, scopes)
			}
		})
	}

	all := ParseScopes(ScopeAll)
	if len(all) != 1 || !all.Has(ScopeAll) || !all.Has("projects:write") {
		t.Fatalf("explicit all-scope sentinel was not preserved: %#v", all)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	_ "embed"
	"encoding/json"
)

// The icons a work level may use (AEON-1142). This document is the one list:
// the web picker imports it and `node scripts/work-icons.mjs --write` copies its
// names into the API enum. Blank stays valid: it draws the level's default.
//
//go:embed work_icons.json
var workIconsDocument []byte

var workIcons = loadWorkIcons(workIconsDocument)

func loadWorkIcons(raw []byte) map[string]bool {
	var doc struct {
		Icons []struct {
			Name string `json:"name"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Icons) == 0 {
		panic("nodes: work_icons.json lists no icons")
	}
	icons := map[string]bool{"": true}
	for _, icon := range doc.Icons {
		icons[icon.Name] = true
	}
	return icons
}

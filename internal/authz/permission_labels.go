// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"strings"
)

type permissionVocabulary struct {
	Resources map[string]string `json:"resources"`
	Actions   map[string]string `json:"actions"`
	Special   map[string]string `json:"special"`
}

// The key dialogs and API/CLI diagnostics assemble the same domain vocabulary.
var permissionWords = assembledPermissionData.Words

func PermissionLabel(key string) string {
	if label := permissionWords.Special[key]; label != "" {
		return label
	}
	resource, action, _ := strings.Cut(key, ".")
	noun := permissionWords.Resources[resource]
	if noun == "" {
		noun = strings.ReplaceAll(resource, "_", " ")
	}
	verb := permissionWords.Actions[action]
	if verb == "" {
		verb = strings.NewReplacer("_", " ", ".", " ").Replace(action)
	}
	if verb != "" {
		verb = strings.ToUpper(verb[:1]) + verb[1:]
	}
	return verb + " " + noun
}

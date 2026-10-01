// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// The key dialogs and API/CLI diagnostics use one vocabulary.
//
//go:embed permission_labels.json
var permissionWordsJSON []byte

type permissionVocabulary struct {
	Resources map[string]string `json:"resources"`
	Actions   map[string]string `json:"actions"`
	Special   map[string]string `json:"special"`
}

var permissionWords = func() permissionVocabulary {
	var words permissionVocabulary
	if err := json.Unmarshal(permissionWordsJSON, &words); err != nil {
		panic("invalid embedded permission labels")
	}
	return words
}()

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

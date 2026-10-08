// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Only domain inputs are committed. Embedding the glob adds a domain without
// changing a shared list of imports or generating a committed aggregate.
//
//go:embed permission_data/*.json
var permissionDataFiles embed.FS

type permissionData struct {
	License                string               `json:"_license"`
	Words                  permissionVocabulary `json:"words"`
	BuiltinAgentExclusions []string             `json:"builtin_agent_exclusions"`
	ProjectSelfPermissions []string             `json:"project_self_permissions"`
}

var assembledPermissionData = loadPermissionData()

func loadPermissionData() permissionData {
	data := permissionData{Words: permissionVocabulary{
		Resources: map[string]string{}, Actions: map[string]string{}, Special: map[string]string{},
	}}
	excluded, self := map[string]bool{}, map[string]bool{}
	files, err := permissionDataFiles.ReadDir("permission_data")
	if err != nil {
		panic(fmt.Sprintf("authz: read permission data: %v", err))
	}
	for _, file := range files {
		raw, err := permissionDataFiles.ReadFile("permission_data/" + file.Name())
		if err != nil {
			panic(fmt.Sprintf("authz: read %s: %v", file.Name(), err))
		}
		var part permissionData
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&part); err != nil {
			panic(fmt.Sprintf("authz: invalid permission data %s: %v", file.Name(), err))
		}
		if decoder.Decode(&struct{}{}) != io.EOF || part.License != "SPDX-License-Identifier: AGPL-3.0-only" {
			panic("authz: invalid permission data " + file.Name())
		}
		registerDeclarations(file.Name(), "resource label", data.Words.Resources, part.Words.Resources)
		registerDeclarations(file.Name(), "action label", data.Words.Actions, part.Words.Actions)
		registerDeclarations(file.Name(), "special label", data.Words.Special, part.Words.Special)
		registerPermissionSet(file.Name(), "built-in agent exclusion", excluded, part.BuiltinAgentExclusions)
		registerPermissionSet(file.Name(), "project self permission", self, part.ProjectSelfPermissions)
	}
	for key := range excluded {
		data.BuiltinAgentExclusions = append(data.BuiltinAgentExclusions, key)
	}
	for key := range self {
		data.ProjectSelfPermissions = append(data.ProjectSelfPermissions, key)
	}
	sort.Strings(data.BuiltinAgentExclusions)
	sort.Strings(data.ProjectSelfPermissions)
	return data
}

func registerPermissionSet(domain, kind string, target map[string]bool, keys []string) {
	for _, key := range keys {
		registerDeclarations(domain, kind, target, map[string]bool{key: true})
	}
}

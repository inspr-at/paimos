// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// Account hand-outs bind to the selected instance's longest matching linked
// folder. Invalid links fail closed rather than becoming project-less work.
func (rt *runtime) accountProject(explicit string) (string, error) {
	if explicit != "" {
		if !validUUID(explicit) {
			return "", usagef("invalid project UUID")
		}
		return explicit, nil
	}
	inst, err := rt.resolve()
	if err != nil {
		return "", err
	}
	if len(inst.ProjectFolders) == 0 {
		return "", nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working folder: %w", err)
	}
	return linkedAccountProject(cwd, inst.ProjectFolders)
}
func linkedAccountProject(cwd string, links map[string]string) (string, error) {
	if len(links) > 1024 {
		return "", fmt.Errorf("too many project folder links")
	}
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	best, project := "", ""
	for folder, id := range links {
		if !filepath.IsAbs(folder) {
			return "", fmt.Errorf("project folder link must be absolute")
		}
		root, err := filepath.EvalSymlinks(folder)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("project folder link unreadable")
		}
		rel, err := filepath.Rel(root, cwd)
		if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
			continue
		}
		if !validUUID(id) {
			return "", fmt.Errorf("project folder link has an invalid UUID")
		}
		if len(root) > len(best) {
			best, project = root, id
		} else if root == best && project != id {
			return "", fmt.Errorf("ambiguous project folder link")
		}
	}
	return project, nil
}

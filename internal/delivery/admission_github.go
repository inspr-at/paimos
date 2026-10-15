// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"github.com/inspr-at/paimos/internal/reviewgate"
)

// GitHub compares expose at most 300 changed files, on the first page only.
// At that boundary keep the merge-base but require visual review. Include both
// sides of renames so moving presentation code into docs cannot exempt it.
// https://docs.github.com/en/rest/commits/commits#compare-two-commits
func readAdmissionDiff(get func(string, any) error, pull *Pull) error {
	if !reviewgate.ValidSHA(pull.Base) || !reviewgate.ValidSHA(pull.Head) {
		return errRead
	}
	var response struct {
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
		Files []struct {
			Name     string `json:"filename"`
			Previous string `json:"previous_filename"`
			Status   string `json:"status"`
		} `json:"files"`
	}
	if err := get("/compare/"+pull.Base+"..."+pull.Head+"?per_page=1&page=1", &response); err != nil {
		return err
	}
	if !reviewgate.ValidSHA(response.MergeBase.SHA) || len(response.Files) > 300 {
		return errRead
	}
	diff := &AdmissionDiff{MergeBase: response.MergeBase.SHA, Complete: response.Files != nil && len(response.Files) < 300, Paths: []string{}}
	for _, file := range response.Files {
		if !validAdmissionPath(file.Name) {
			return errRead
		}
		diff.Paths = append(diff.Paths, file.Name)
		switch file.Status {
		case "added", "removed", "modified", "changed", "unchanged":
		case "renamed", "copied":
			if !validAdmissionPath(file.Previous) {
				return errRead
			}
			diff.Paths = append(diff.Paths, file.Previous)
		default:
			diff.Complete = false
		}
	}
	pull.AdmissionDiff = diff
	return nil
}

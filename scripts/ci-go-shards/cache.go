// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/ciproof"
)

// Reuse only byte-pinned, runtime-free audits from AEON-417. Tests that read
// databases, child scripts or other packages' files always execute fresh.
// Changed audits or package inventories also execute fresh; main never reuses
// test results. This deliberately starts smaller than Go's implicit cache.
func auditedCachePackages(root string) map[string]bool {
	allowed := map[string]bool{}
	body, err := os.ReadFile(filepath.Join(root, "internal/ciproof/go-impact-policy.json"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != "7ff7b13b9814c08038694d4d60585b2d2a83c2ee1bb881d33c72f3178c491a78" {
		return allowed
	}
	var policy struct {
		Audits []struct {
			Package      string
			Files        []struct{ Path, Digest string }
			RuntimePaths []string `json:"runtime_paths"`
		}
	}
	if json.Unmarshal(body, &policy) != nil {
		return allowed
	}
	for _, audit := range policy.Audits {
		if len(audit.RuntimePaths) != 0 {
			continue
		}
		relative := strings.TrimPrefix(audit.Package, "github.com/inspr-at/paimos/")
		entries, err := os.ReadDir(filepath.Join(root, relative))
		if err != nil || len(entries) != len(audit.Files) {
			continue
		}
		valid := true
		for _, file := range audit.Files {
			bytes, err := os.ReadFile(filepath.Join(root, file.Path))
			if err != nil || ciproof.Hash("blob", bytes) != file.Digest {
				valid = false
				break
			}
		}
		if valid {
			allowed[audit.Package] = true
		}
	}
	return allowed
}

func cachePlan(root string, plan shardPlan, impacted bool) (shardPlan, shardPlan) {
	if !impacted {
		return plan, shardPlan{}
	}
	allowed := auditedCachePackages(root)
	fresh, cached := shardPlan{runs: plan.runs}, shardPlan{}
	for _, path := range plan.whole {
		if allowed[path] {
			cached.whole = append(cached.whole, path)
		} else {
			fresh.whole = append(fresh.whole, path)
		}
	}
	return fresh, cached
}

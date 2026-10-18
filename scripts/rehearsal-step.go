//go:build ignore

// SPDX-License-Identifier: AGPL-3.0-only
// Print an allowlisted production shell step, so rehearsal executes the exact
// assets/gh body instead of a hand-copied approximation. No shell runs here.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "inventory" {
		var document struct {
			Jobs map[string]struct{ Steps []map[string]any }
		}
		document.Jobs = make(map[string]struct{ Steps []map[string]any })
		for _, name := range []string{"release.yml", "release-completion.yml"} {
			data, err := os.ReadFile(".github/workflows/" + name)
			if err != nil {
				panic(err)
			}
			var part struct {
				Jobs map[string]struct{ Steps []map[string]any }
			}
			if err := yaml.Unmarshal(data, &part); err != nil {
				panic(err)
			}
			for id, job := range part.Jobs {
				if _, exists := document.Jobs[id]; exists {
					panic("duplicate release job")
				}
				document.Jobs[id] = job
			}
		}
		inventory := map[string][]map[string]string{}
		for id, job := range document.Jobs {
			inventory[id] = []map[string]string{}
			for _, step := range job.Steps {
				body, err := json.Marshal(step)
				if err != nil {
					panic(err)
				}
				name, _ := step["name"].(string)
				if name == "" {
					name, _ = step["uses"].(string)
				}
				inventory[id] = append(inventory[id], map[string]string{"step": name,
					"sha256": fmt.Sprintf("%x", sha256.Sum256(body)), "mode": "", "counterpart": ""})
			}
		}
		output, err := json.MarshalIndent(inventory, "", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Println(string(output))
		return
	}
	if len(os.Args) != 3 {
		panic("usage: rehearsal-step.go JOB STEP")
	}
	allowed := map[string]bool{
		"assets/Build linux paimos-agentd and aeon-cli": true,
		"image/Publish multi-arch index":                true,
		"image/Verify pushed image attestation":         true,
	}
	if !allowed[os.Args[1]+"/"+os.Args[2]] {
		panic("step has no safe rehearsal adapter")
	}
	file := "release.yml"
	if os.Args[1] == "assets" {
		file = "release-completion.yml"
	}
	data, err := os.ReadFile(".github/workflows/" + file)
	if err != nil {
		panic(err)
	}
	var document struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		panic(err)
	}
	for _, step := range document.Jobs[os.Args[1]].Steps {
		if step.Name == os.Args[2] && step.Run != "" {
			fmt.Print(step.Run)
			return
		}
	}
	panic("production step is missing")
}

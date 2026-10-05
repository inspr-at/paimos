// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The runtime UID/GID is a contract with the host: csb1's aeon-files
// directory is owned by 65532. v260924170915 lost file access when the image
// user floated to an alpine default (UID 100). The last USER in the runtime
// stage is the one the image runs as; an earlier 65532 line does not count.
func TestDockerfileRuntimeUserIs65532(t *testing.T) {
	assembly, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	closure, err := os.ReadFile("../../scripts/Dockerfile.runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDockerfileRuntimeIdentity(string(assembly), string(closure)); err != nil {
		t.Fatal(err)
	}
	// Mutate the real recipes so comments, dead stages and an earlier USER
	// cannot substitute for the instructions that produce the final image.
	for _, tc := range []struct {
		name, recipe, old, replacement, want string
	}{
		{"missing user", "assembly", "USER 65532:65532", "", "runtime USER"},
		{"commented user", "assembly", "USER 65532:65532", "# USER 65532:65532", "runtime USER"},
		{"floating user", "assembly", "USER 65532:65532", "USER aeon", "runtime USER"},
		{"wrong uid", "assembly", "USER 65532:65532", "USER 100:65532", "runtime USER"},
		{"wrong gid", "assembly", "USER 65532:65532", "USER 65532:100", "runtime USER"},
		{"later user wins", "assembly", "USER 65532:65532", "USER 65532:65532\nUSER root", "runtime USER"},
		{"user in dead stage", "assembly", "USER 65532:65532", "USER 65532:65532\nFROM aeon-runtime", "runtime USER"},
		{"wrong runtime context", "assembly", "FROM aeon-runtime", "FROM alpine:latest", "assembly base"},
		{"floating uid", "closure", "-u 65532 ", "", "create uid 65532"},
		{"wrong created uid", "closure", "-u 65532 ", "-u 100 ", "create uid 65532"},
		{"floating gid", "closure", "-g 65532 ", "", "create gid 65532"},
		{"wrong created gid", "closure", "-g 65532 ", "-g 100 ", "create gid 65532"},
		{"wrong group name", "closure", "-g 65532 aeon", "-g 65532 other", "create gid 65532"},
		{"wrong user group", "closure", "-G aeon aeon", "-G other aeon", "create uid 65532"},
		{"wrong user name", "closure", "-G aeon aeon", "-G aeon other", "create uid 65532"},
		{"creation in comment", "closure", "RUN apk", "# RUN apk", "create gid 65532"},
		{"creation in dead stage", "closure", "-G aeon aeon", "-G aeon aeon\nFROM alpine:latest", "create gid 65532"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := string(assembly), string(closure)
			if tc.recipe == "assembly" {
				a = strings.Replace(a, tc.old, tc.replacement, 1)
			} else {
				c = strings.Replace(c, tc.old, tc.replacement, 1)
			}
			if a == string(assembly) && c == string(closure) {
				t.Fatal("mutation did not change a recipe")
			}
			if err := checkDockerfileRuntimeIdentity(a, c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want rejection %q, got %v", tc.want, err)
			}
		})
	}
}

// This guard deliberately checks executable instructions in the final stages,
// not matching text in annotations or stages that the image never inherits.
func checkDockerfileRuntimeIdentity(assembly, closure string) error {
	image := dockerfileFinalInstructions(assembly)
	if len(image) == 0 || len(image[0]) < 2 || !strings.EqualFold(image[0][0], "FROM") || image[0][1] != "aeon-runtime" {
		return fmt.Errorf("assembly base must be the frozen aeon-runtime context")
	}
	var lastUser string
	for _, instruction := range image {
		if strings.EqualFold(instruction[0], "USER") {
			lastUser = strings.Join(instruction[1:], " ")
		}
	}
	if lastUser != "65532:65532" {
		return fmt.Errorf("runtime USER %q, want 65532:65532", lastUser)
	}
	var group, user bool
	for _, instruction := range dockerfileFinalInstructions(closure) {
		if !strings.EqualFold(instruction[0], "RUN") {
			continue
		}
		for _, command := range strings.Split(strings.Join(instruction[1:], " "), "&&") {
			switch strings.Join(strings.Fields(command), " ") {
			case "addgroup -S -g 65532 aeon":
				group = true
			case "adduser -S -D -u 65532 -G aeon aeon":
				user = true
			}
		}
	}
	if !group {
		return fmt.Errorf("runtime closure must create gid 65532 named aeon")
	}
	if !user {
		return fmt.Errorf("runtime closure must create uid 65532 named aeon in group aeon")
	}
	return nil
}

func dockerfileFinalInstructions(recipe string) [][]string {
	recipe = strings.ReplaceAll(recipe, "\r\n", "\n")
	recipe = strings.ReplaceAll(recipe, "\\\n", " ")
	var instructions [][]string
	for _, line := range strings.Split(recipe, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if strings.EqualFold(fields[0], "FROM") {
			instructions = nil
		}
		instructions = append(instructions, fields)
	}
	return instructions
}

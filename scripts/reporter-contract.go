// SPDX-License-Identifier: AGPL-3.0-only

// Run `go run scripts/reporter-contract.go since <git-ref>` before deployment
// to announce reporter-facing schema/version changes to Pharos and Janus.
// Run `go run scripts/reporter-contract.go pin` only after making the required
// contract version bump and reviewing the changed response schema.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/inspr-at/paimos/internal/reportercontract"
)

const pinsPath = "internal/reportercontract/pins.json"

func main() {
	if len(os.Args) < 2 {
		die("usage: go run scripts/reporter-contract.go since <git-ref> | pin")
	}
	switch os.Args[1] {
	case "since":
		if len(os.Args) != 3 {
			die("since requires one git ref")
		}
		current, err := readPins(pinsPath)
		if err != nil {
			die("read current pins: %v", err)
		}
		cmd := exec.Command("git", "show", os.Args[2]+":"+pinsPath)
		raw, err := cmd.Output()
		if err != nil {
			verify := exec.Command("git", "rev-parse", "--verify", os.Args[2]+"^{commit}")
			if verify.Run() != nil {
				die("invalid git ref %s", os.Args[2])
			}
			raw = []byte("{}") // A pre-contract ref has no pins: every surface is new.
		}
		var old map[string]reportercontract.Pin
		if err := json.Unmarshal(raw, &old); err != nil {
			die("parse old pins: %v", err)
		}
		fmt.Printf("reporter-facing contract changes since %s\n", os.Args[2])
		names := make([]string, 0, len(current))
		for name := range current {
			names = append(names, name)
		}
		sort.Strings(names)
		changed := false
		for _, name := range names {
			now := current[name]
			before, ok := old[name]
			if ok && before.SHA256 == now.SHA256 && before.Version == now.Version {
				continue
			}
			changed = true
			if !ok {
				fmt.Printf("%s: new %s (%s)\n", name, now.Version, now.SHA256)
				continue
			}
			bump, err := reportercontract.RequiredBump(before, now)
			if err != nil {
				die("compare %s: %v", name, err)
			}
			fmt.Printf("%s: %s -> %s; response %s; SHA256 %s -> %s\n", name, before.Version, now.Version, bump, before.SHA256, now.SHA256)
		}
		if !changed {
			fmt.Println("none")
		}
	case "pin":
		openAPI, err := os.ReadFile("api/openapi.yaml")
		if err != nil {
			die("read OpenAPI: %v", err)
		}
		pins, err := reportercontract.Current(openAPI)
		if err != nil {
			die("pin response schema: %v", err)
		}
		raw, err := json.MarshalIndent(pins, "", "  ")
		if err != nil {
			die("marshal pins: %v", err)
		}
		if err := os.WriteFile(pinsPath, append(raw, '\n'), 0644); err != nil {
			die("write pins: %v", err)
		}
	default:
		die("unknown command %q", os.Args[1])
	}
}

func readPins(path string) (map[string]reportercontract.Pin, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pins map[string]reportercontract.Pin
	err = json.Unmarshal(raw, &pins)
	return pins, err
}

func die(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }

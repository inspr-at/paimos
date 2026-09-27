// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) harnessProvenance() *Command {
	var project, session, agent, leaseFile, promptVersion, promptSHA string
	var instructions, versions []string
	var show bool
	return &Command{Name: "provenance", Short: "Record instruction hashes for one session", Use: "harness provenance --project KEY --session UUID --agent NAME --worker-lease-file PATH --instruction FILE", addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key")
		fs.string(&session, "session", 0, "public session UUID")
		fs.string(&agent, "agent", 0, "attributed agent name")
		fs.string(&leaseFile, "worker-lease-file", 0, "private generation lease file")
		fs.strings(&instructions, "instruction", "explicit AGENTS.md, CLAUDE.md or SKILL.md file")
		fs.strings(&versions, "instruction-version", "LOGICAL=VERSION for a file named by --instruction")
		fs.string(&promptVersion, "prompt-template-version", 0, "prompt template version identifier")
		fs.string(&promptSHA, "prompt-template-sha256", 0, "explicit lowercase sha256 of the template bytes; omit to record the version with no content digest")
		fs.bool(&show, "show", 0, "print recorded provenance")
	}, run: func([]string) error {
		if show && (len(instructions) > 0 || len(versions) > 0 || promptVersion != "" || promptSHA != "") {
			return usagef("--show only reads recorded provenance")
		}
		if !validUUID(session) {
			return usagef("--session UUID is required")
		}
		var items []harness.ProvenanceItem
		if !show {
			var err error
			items, err = harness.CollectInstructionFiles(instructions)
			if err != nil {
				return usagef("%s", err.Error())
			}
			if promptVersion != "" || promptSHA != "" {
				if promptVersion == "" {
					return usagef("prompt template digest requires --prompt-template-version")
				}
				item, e := harness.PromptTemplateProvenance(promptVersion, promptSHA)
				if e != nil {
					return usagef("%s", e.Error())
				}
				items = append(items, item)
			}
			if len(items) == 0 {
				return usagef("pass at least one --instruction or --prompt-template-version")
			}
			for _, spec := range versions {
				logical, version, ok := strings.Cut(spec, "=")
				if !ok || logical == "" || version == "" {
					return usagef("--instruction-version must be LOGICAL=VERSION")
				}
				items, err = harness.SetProvenanceVersion(items, logical, version)
				if err != nil {
					return usagef("%s", err.Error())
				}
			}
		}
		id, err := rt.harnessProject(project)
		if err != nil {
			return err
		}
		path := harnessPath(id, session) + "/provenance"
		if show {
			var out any
			if err = rt.harnessDo(http.MethodGet, path, "", nil, &out); err != nil {
				return err
			}
			return rt.printHarness(out)
		}
		if !agentNameRE.MatchString(agent) {
			return usagef("--agent is required")
		}
		me, err := rt.caller()
		if err != nil {
			return err
		}
		if me.Principal.Name != agent {
			return usagef("--agent must name the authenticated principal")
		}
		lease, err := rt.harnessSecret(leaseFile, "worker-lease-file")
		if err != nil {
			return err
		}
		if len(lease) < 32 {
			return usagef("worker lease must contain at least 32 characters")
		}
		var out any
		if err = rt.harnessDo(http.MethodPost, path, lease, map[string]any{"items": items}, &out); err != nil {
			return err
		}
		return rt.printHarness(out)
	}}
}

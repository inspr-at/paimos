// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

// cmdRulesImport is intentionally standalone while AR1 owns rules and root
// dispatch. The coordinator registration patch adds it as `aeon rules-import`.
func (rt *runtime) cmdRulesImport() *Command {
	return rt.rulesImportCommand(rt.api)
}

func (rt *runtime) rulesImportCommand(connect func() (*client.Client, error)) *Command {
	var files, layerFlags []string
	var trust, section, setID, revision, reportDir string
	var apply bool
	return &Command{
		Name: "rules-import", Short: "Preview explicit doctrine files; optionally update a revision-checked draft",
		Use:     "rules-import --context template|private|project|person --file PATH [--file PATH...] [--layer PATH=company|project|person|agent] [--section all|personal|kernel] [--report DIR] [--apply --set UUID --revision N]",
		Long:    "Preview is local and prints JSON without loading API credentials. An optional report directory receives contradictions.md and contradictions.json (same-identity differences and conflicting directives across layers). Apply requires an existing set and expected revision, uses the configured instance authorization, and prints a draft receipt after the proposal. Matching content hashes are unchanged. A changed import hash updates that draft rule. Rules marked edited here, unrelated draft rules, unresolved choices and on-demand placement are not overwritten or published. No publish or restore operation is available.",
		minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.strings(&files, "file", "explicit known doctrine file (repeatable)")
			fs.strings(&layerFlags, "layer", "explicit file-to-layer mapping PATH=company|project|person|agent (repeatable)")
			fs.string(&trust, "context", 0, "explicit trust context: template, private, project, person")
			fs.string(&section, "section", 0, "all (default), personal, kernel")
			fs.string(&reportDir, "report", 0, "directory for contradictions.md and contradictions.json; existing files are refused")
			fs.bool(&apply, "apply", 0, "request a draft write; API authorization is still required")
			fs.string(&setID, "set", 0, "existing AR1 set UUID (apply only)")
			fs.string(&revision, "revision", 0, "explicit expected draft revision (apply only)")
		},
		run: func([]string) error {
			var n int64
			if revision != "" {
				var err error
				n, err = strconv.ParseInt(revision, 10, 64)
				if err != nil || n < 1 {
					return usagef("--revision requires a positive integer")
				}
			}
			layers, err := parseLayerFlags(layerFlags)
			if err != nil {
				return err
			}
			return rulesimport.Run(context.Background(), rulesimport.Options{
				Request:   rulesimport.Request{Context: rulesimport.TrustContext(trust), Section: section, Files: files, Layers: layers},
				Apply:     apply,
				Target:    rulesimport.Target{SetID: setID, Revision: n},
				ReportDir: reportDir,
			}, rt.stdout, connect)
		},
	}
}

func parseLayerFlags(items []string) (map[string]rulesimport.Layer, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make(map[string]rulesimport.Layer, len(items))
	for _, item := range items {
		path, layer, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(path) == "" {
			return nil, usagef("--layer requires PATH=company|project|person|agent")
		}
		switch rulesimport.Layer(layer) {
		case rulesimport.LayerCompany, rulesimport.LayerProject, rulesimport.LayerPerson, rulesimport.LayerAgent:
		default:
			return nil, usagef("--layer requires PATH=company|project|person|agent")
		}
		if _, exists := out[path]; exists {
			return nil, usagef("--layer path repeated")
		}
		out[path] = rulesimport.Layer(layer)
	}
	return out, nil
}

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulescompare"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

// cmdRulesCompare is an offline one-time report. It does not publish, wait,
// or replace AGENTS.md / CLAUDE.md.
func (rt *runtime) cmdRulesCompare() *Command {
	var files []string
	var trust, section, mergedPath, provenancePath, sessionID, outPath string
	return &Command{
		Name:    "rules-compare",
		Short:   "Compare explicit instruction files with merged rules and receipt hashes",
		Use:     "rules-compare --context template|private|project|person --file PATH [--file PATH...] [--section all|personal|kernel] [--merged FILE.json] [--provenance FILE.json --session SESSION_UUID] [--out FILE.json]",
		Long:    "Offline and one-time. Named files are expected inputs and are hashed with the doctrine importer's descriptor-safe reader. Canonical AEON-219 revision metadata needs an explicit matching session ID for a worker-reported receipt comparison. Offline JSON does not prove API origin, publication, runtime execution, model load or obedience. Proposal-shaped provenance is shown separately as unverified. A supplied merge can show historical differences but cannot authorize rollout. Nothing is installed over an active instruction file, and there is no waiting window.",
		minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.strings(&files, "file", "explicit doctrine file (repeatable); no discovery")
			fs.string(&trust, "context", 0, "explicit trust context: template, private, project, person")
			fs.string(&section, "section", 0, "all (default), personal, kernel")
			fs.string(&mergedPath, "merged", 0, "optional AR1 merged-rules JSON from an explicit private file")
			fs.string(&provenancePath, "provenance", 0, "optional AEON-219 provenance JSON from an explicit private file")
			fs.string(&sessionID, "session", 0, "expected harness session UUID for provenance attribution")
			fs.string(&outPath, "out", 0, "optional new report JSON; never overwrites")
		},
		run: func([]string) error {
			if trust == "" || len(files) == 0 {
				return usagef("rules-compare requires --context and at least one --file")
			}
			var merged, provenance []byte
			var err error
			if mergedPath != "" {
				if merged, err = rules.ReadFile(mergedPath, rules.MaxCacheBytes); err != nil {
					return err
				}
			}
			if provenancePath != "" {
				if provenance, err = rules.ReadFile(provenancePath, rules.MaxCacheBytes); err != nil {
					return err
				}
			}
			report, err := rulescompare.Compare(context.Background(), rulescompare.Input{
				Context: rulesimport.TrustContext(trust), Section: section, Files: files,
				Merged: merged, Provenance: provenance, ExpectedSessionID: sessionID,
			})
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err = enc.Encode(report); err != nil {
				return err
			}
			if buf.Len() > rules.MaxCacheBytes {
				return usagef("comparison report exceeds the byte bound")
			}
			if outPath != "" {
				if err = rules.WriteFile(outPath, buf.Bytes(), false); err != nil {
					return err
				}
			}
			_, err = rt.stdout.Write(buf.Bytes())
			return err
		},
	}
}

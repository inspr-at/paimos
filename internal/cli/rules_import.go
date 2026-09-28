// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"strconv"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

// cmdRulesImport is intentionally standalone while AR1 owns rules and root
// dispatch. The coordinator registration patch adds it as `aeon rules-import`.
func (rt *runtime) cmdRulesImport() *Command {
	return rt.rulesImportCommand(rt.api)
}

func (rt *runtime) rulesImportCommand(connect func() (*client.Client, error)) *Command {
	var files []string
	var trust, section, setID, revision string
	var apply bool
	return &Command{
		Name: "rules-import", Short: "Preview explicit doctrine files; optionally add to a revision-checked draft",
		Use:     "rules-import --context template|private|project|person --file PATH [--file PATH...] [--section all|personal|kernel] [--apply --set UUID --revision N]",
		Long:    "Preview is local and prints JSON without loading API credentials. Apply requires an existing set and expected revision, uses the configured instance authorization, and prints a draft receipt after the proposal. Unrelated draft rules are retained; differing identities, unresolved choices and on-demand placement are refused. No publish or restore operation is available.",
		minArgs: 0, maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.strings(&files, "file", "explicit known doctrine file (repeatable)")
			fs.string(&trust, "context", 0, "explicit trust context: template, private, project, person")
			fs.string(&section, "section", 0, "all (default), personal, kernel")
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
			return rulesimport.Run(context.Background(), rulesimport.Options{
				Request: rulesimport.Request{Context: rulesimport.TrustContext(trust), Section: section, Files: files},
				Apply:   apply, Target: rulesimport.Target{SetID: setID, Revision: n},
			}, rt.stdout, connect)
		},
	}
}

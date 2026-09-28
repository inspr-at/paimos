// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

// Run is the standalone importer command. Preview is the default.
// Registration in the shared aeon CLI belongs to AR1; this function does not edit it.
// Apply never publishes and cannot authorize itself.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	for _, arg := range args {
		if arg == "--publish" || arg == "publish" {
			return fmt.Errorf("%w: importer does not publish rules", ErrPublishRefused)
		}
	}
	cmd, rest := splitCommand(args)
	if cmd == "" {
		return usage()
	}
	fs := flag.NewFlagSet("rulesimport "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var files stringList
	contextFlag := fs.String("context", "", "trust context: template, private, project, or person")
	section := fs.String("section", SectionAll, "section: all, personal, or kernel")
	tenant := fs.String("tenant", "", "tenant slug, required for apply and ignored for the write")
	version := fs.String("version", "", "refused publication coordinate")
	draft := fs.String("ar1-draft", "", "optional explicit AR1 draft file; contents are not interpreted as a contract")
	fs.Var(&files, "file", "explicit doctrine file (repeatable)")
	if err := fs.Parse(rest); err != nil {
		return usage()
	}
	if fs.NArg() != 0 {
		return usage()
	}
	if *version != "" {
		if !releasehistory.ValidVersion(*version) {
			return fmt.Errorf("%w: publication coordinate is not inspr-calendar-v2", ErrPublishRefused)
		}
		return fmt.Errorf("%w: importer does not publish ruleset versions", ErrPublishRefused)
	}
	proposal, err := Build(ctx, Request{
		Context:  TrustContext(*contextFlag),
		Section:  *section,
		Files:    append([]string(nil), files...),
		AR1Draft: *draft,
	})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", encoded); err != nil {
		return err
	}
	if cmd == "preview" {
		return nil
	}
	if !validTenant(*tenant) {
		return fmt.Errorf("%w: apply requires one tenant slug", ErrDraftUnavailable)
	}
	return ImportDraft(ctx, DraftRequest{
		Tenant:     *tenant,
		Context:    proposal.Context,
		Authorized: false,
		Proposal:   proposal,
	}, nil)
}

func splitCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "preview", "apply":
		return args[0], args[1:]
	case "-h", "--help", "help":
		return "", nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return "preview", args
		}
		return "", nil
	}
}

func usage() error {
	return fmt.Errorf("usage: rulesimport preview|apply --context template|private|project|person --file PATH [--file PATH...] [--section all|personal|kernel] [--tenant SLUG]")
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

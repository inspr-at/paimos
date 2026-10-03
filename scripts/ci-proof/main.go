// SPDX-License-Identifier: AGPL-3.0-only

// ci-proof reads immutable objects from a dedicated bare mirror and emits shadow
// diagnostics. It never runs tests, fetches refs, publishes checks or enables CI
// omissions. Digest output is a review aid, not an approved controller pin.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/inspr-at/paimos/internal/ciproof"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "digest" && args[0] != "shadow") {
		return fmt.Errorf("usage: ci-proof digest|shadow --mirror ABSOLUTE_BARE_REPO --policy-commit SHA [--policy-digest SHA256 --environment-digest SHA256 --binding FILE --ledger FILE --receipt FILE]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	mirror := f.String("mirror", "", "controller-owned absolute bare Git mirror path")
	git := f.String("git", "", "approved absolute Git executable path (default: installed Git)")
	commit := f.String("policy-commit", "", "reviewed immutable policy commit")
	pin := f.String("policy-digest", "", "independently reviewed expected policy digest")
	environment := f.String("environment-digest", "", "pinned environment/security epoch closure digest")
	bindingPath := f.String("binding", "", "resolved immutable event binding JSON, diagnostic until B authenticates it")
	ledger := f.String("ledger", "", "optional private shadow ledger in a controller-owned directory")
	receiptPath := f.String("receipt", "", "optional unsigned diagnostic receipt (requires ledger)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *git == "" {
		installed, err := exec.LookPath("git")
		if err != nil {
			return err
		}
		absolute, err := filepath.Abs(installed)
		if err != nil {
			return err
		}
		*git = absolute
	}
	r, err := ciproof.OpenRepository(ctx, *mirror, *git)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if args[0] == "digest" {
		if *pin != "" || *bindingPath != "" || *ledger != "" || *receiptPath != "" || *environment != "" {
			return fmt.Errorf("digest does not accept plan/publication options")
		}
		digest, err := ciproof.PolicyDigest(ctx, r, *commit)
		if err != nil {
			return err
		}
		return enc.Encode(ciproof.Pin{Commit: *commit, Digest: digest})
	}
	if *bindingPath == "" {
		return fmt.Errorf("binding file required")
	}
	b, err := os.ReadFile(*bindingPath)
	if err != nil {
		return err
	}
	var binding ciproof.Binding
	if err := ciproof.Decode("binding", b, &binding); err != nil {
		return err
	}
	plan, err := ciproof.NewPlan(ctx, r, ciproof.Pin{Commit: *commit, Digest: *pin}, binding, *environment)
	if err != nil {
		return err
	}
	if *receiptPath != "" && *ledger == "" {
		return fmt.Errorf("receipt observation requires a ledger")
	}
	if *ledger != "" {
		var receipt *ciproof.Receipt
		if *receiptPath != "" {
			b, err := os.ReadFile(*receiptPath)
			if err != nil {
				return err
			}
			receipt = &ciproof.Receipt{}
			if err := ciproof.Decode("receipt", b, receipt); err != nil {
				return err
			}
		}
		if _, err := ciproof.WithLedger(ctx, r, *ledger, plan, receipt); err != nil {
			return err
		}
	}
	return enc.Encode(plan)
}

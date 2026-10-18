// SPDX-License-Identifier: AGPL-3.0-only

// ci-authority is an independently installed external shadow controller. It
// reads GitHub and emits diagnostics; it has no check-publication implementation.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/inspr-at/paimos/internal/ciproof"
)

type config struct {
	Schema             string                  `json:"schema"`
	Mirror             string                  `json:"mirror"`
	Git                ciproof.FilePin         `json:"git"`
	Authority          ciproof.AuthorityConfig `json:"authority"`
	Profile            ciproof.VMProfile       `json:"profile"`
	AdmissionPublicKey []byte                  `json:"admission_public_key"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		// No transport errors, environment, webhook, source code or guest output.
		fmt.Fprintln(os.Stderr, "CI shadow authority refused incomplete or invalid inputs; full CI remains required")
		os.Exit(1)
	}
}

func readJSON(name string, dst any) error {
	if !filepath.IsAbs(name) {
		return fmt.Errorf("absolute controller input path required")
	}
	f, err := os.Open(name)
	if err != nil {
		return fmt.Errorf("controller input unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return fmt.Errorf("controller input limit")
	}
	return ciproof.DecodeControllerInput(raw, dst)
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "shadow" && args[0] != "observe") {
		return fmt.Errorf("usage: ci-authority shadow|observe --config ABSOLUTE_JSON --event pull_request|merge_group|push --delivery ID --signature SHA256 --webhook ABSOLUTE_JSON --generation N [--admission ABSOLUTE_JSON --obligation ID --run-id N --job-id ID]")
	}
	if os.Getenv("GITHUB_ACTIONS") != "" {
		return fmt.Errorf("external authority cannot run in Actions")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configPath := f.String("config", "", "independently installed controller configuration, with public pins only")
	event := f.String("event", "", "authenticated GitHub event type")
	delivery := f.String("delivery", "", "authenticated delivery identity")
	signature := f.String("signature", "", "X-Hub-Signature-256 (not a credential)")
	webhookPath := f.String("webhook", "", "unaltered GitHub webhook body")
	generation := f.Int64("generation", 0, "externally allocated monotonic generation")
	admissionPath := f.String("admission", "", "independent signed whole-executable-tree admission")
	obligation := f.String("obligation", "", "one controller-owned obligation recipe")
	runID := f.Int64("run-id", 0, "new complete execution run identity")
	jobID := f.String("job-id", "", "recorded approved launcher job identity")
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
		return fmt.Errorf("invalid controller arguments")
	}
	if args[0] == "shadow" && (*admissionPath != "" || *obligation != "" || *runID != 0 || *jobID != "") {
		return fmt.Errorf("shadow cannot launch execution")
	}
	var c config
	if err := readJSON(*configPath, &c); err != nil {
		return err
	}
	if c.Schema != "aeon.ci.authority-config.v1" {
		return fmt.Errorf("unknown controller config")
	}
	if !filepath.IsAbs(*webhookPath) {
		return fmt.Errorf("absolute webhook path required")
	}
	w, err := os.Open(*webhookPath)
	if err != nil {
		return fmt.Errorf("webhook unavailable")
	}
	defer w.Close()
	raw, err := io.ReadAll(io.LimitReader(w, (2<<20)+1))
	if err != nil {
		return fmt.Errorf("webhook read failed")
	}
	e, err := ciproof.AuthenticateEvent(*event, *delivery, *signature, raw, []byte(os.Getenv("AEON_CI_WEBHOOK_SECRET")))
	if err != nil {
		return err
	}
	api, err := ciproof.NewGitHubReader(c.Authority.Repository, os.Getenv("AEON_CI_READ_TOKEN"))
	if err != nil {
		return err
	}
	r, err := ciproof.OpenAuthorityRepository(ctx, *configPath, c.Mirror, c.Git)
	if err != nil {
		return err
	}
	a, err := ciproof.NewAuthority(c.Authority, r, api)
	if err != nil {
		return err
	}
	p, err := a.Plan(ctx, e, *generation)
	if err != nil {
		return err
	}
	var supervisor *ciproof.Supervisor
	observations := []ciproof.SupervisedObservation{}
	if args[0] == "observe" {
		var admission ciproof.Admission
		if err := readJSON(*admissionPath, &admission); err != nil {
			return err
		}
		supervisor, err = ciproof.NewSupervisor(r, c.Profile, ed25519.PublicKey(c.AdmissionPublicKey))
		if err != nil {
			return err
		}
		o, err := supervisor.Run(ctx, p, *obligation, admission, ciproof.ExecutionIdentity{RunID: *runID, Attempt: 1, JobID: *jobID})
		if err != nil {
			return err
		}
		observations = append(observations, o)
	}
	checks, err := a.Checks(ctx, e, p, supervisor, observations)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Mode         string                          `json:"mode"`
		Plan         ciproof.Plan                    `json:"plan"`
		Checks       []ciproof.CheckProposal         `json:"checks"`
		Observations []ciproof.SupervisedObservation `json:"observations"`
	}{"shadow", p, checks, observations})
}

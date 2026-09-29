// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/deploytarget"
)

// Aeon stage handoffs are server-owned and fenced. The classic credential,
// registration and launch subcommands cannot be translated into authority.
func (rt *runtime) cmdExternalStage() *Command {
	var project, release, stage, operation, key string
	var revision int
	var reportFile, targetFile string
	request := &Command{Name: "request", Short: "Request a server-fenced stage handoff", Use: "external-stage request --project KEY --release KEY --stage deploy|access --operation OP --expected-journey-revision N --idempotency-key KEY [--target-file JSON]",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key")
			fs.string(&release, "release", 0, "release node key")
			fs.string(&stage, "stage", 0, "deploy or access")
			fs.string(&operation, "operation", 0, "prepare, apply, deploy, or verify")
			fs.int(&revision, "expected-journey-revision", "journey revision")
			fs.string(&key, "idempotency-key", 0, "request idempotency key")
			fs.string(&targetFile, "target-file", 0, "explicit DeployTarget JSON file or - for stdin; optional for deploy, omitted otherwise")
		}, run: func([]string) error {
			if project == "" || release == "" || key == "" || revision < 1 {
				return usagef("--project, --release, --expected-journey-revision and --idempotency-key are required")
			}
			if stage != "deploy" && stage != "access" {
				return usagef("--stage must be deploy or access")
			}
			switch operation {
			case "prepare", "apply", "deploy", "verify":
			default:
				return usagef("invalid --operation")
			}
			var target *deploytarget.Target
			if operation == "deploy" && targetFile != "" {
				var err error
				target, err = rt.readDeployTarget(targetFile)
				if err != nil {
					return err
				}
			} else if targetFile != "" {
				return usagef("--target-file is only valid for deploy")
			}
			p, err := rt.projectNode(project)
			if err != nil {
				return err
			}
			r, err := rt.nodeRef(release)
			if err != nil {
				return err
			}
			body := map[string]any{"project_node_id": p.ID, "release_node_id": r.ID, "stage": stage, "operation": operation, "expected_journey_revision": revision, "idempotency_key": key}
			if target != nil {
				body["target"] = target
			}
			var result map[string]any
			if err := rt.do(http.MethodPost, "/api/stage-handoffs", body, &result); err != nil {
				return err
			}
			return rt.printJSON(result)
		}}
	return &Command{Name: "external-stage", Short: "Inspect or request Aeon stage handoffs", Use: "external-stage <request|pull|report|result>", subs: []*Command{
		request,
		{Name: "pull", Short: "Read safe handoff state", Use: "external-stage pull <handoff-id>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
			if !validUUID(args[0]) {
				return usagef("handoff id must be an Aeon UUID")
			}
			var result map[string]any
			if err := rt.do(http.MethodGet, "/api/stage-handoffs/"+args[0], nil, &result); err != nil {
				return err
			}
			return rt.printJSON(result)
		}},
		{Name: "report", Short: "Append typed stage evidence", Use: "external-stage report <handoff-id> --report-file JSON", minArgs: 1, maxArgs: 1,
			addFlags: func(fs *flagSet) {
				fs.string(&reportFile, "report-file", 0, "Aeon StageEvidenceWrite JSON file or - for stdin")
			}, run: func(args []string) error {
				if !validUUID(args[0]) {
					return usagef("handoff id must be an Aeon UUID")
				}
				if reportFile == "" {
					return usagef("--report-file is required")
				}
				var raw []byte
				var err error
				if reportFile == "-" {
					raw, err = io.ReadAll(io.LimitReader(rt.stdin, 1<<20+1))
				} else {
					raw, err = os.ReadFile(reportFile)
				}
				if err != nil {
					return rt.fail(err, "")
				}
				if len(raw) > 1<<20 {
					return usagef("report is too large")
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					return usagef("invalid report JSON: %v", err)
				}
				var result map[string]any
				if err := rt.do(http.MethodPost, "/api/stage-handoffs/"+args[0]+"/evidence", body, &result); err != nil {
					return err
				}
				return rt.printJSON(result)
			}},
		{Name: "result", Short: "Submit a terminal stage result", Use: "external-stage result <handoff-id> --report-file JSON", minArgs: 1, maxArgs: 1,
			addFlags: func(fs *flagSet) {
				fs.string(&reportFile, "report-file", 0, "Aeon StageResultWrite JSON file or - for stdin")
			}, run: func(args []string) error {
				if !validUUID(args[0]) {
					return usagef("handoff id must be an Aeon UUID")
				}
				if reportFile == "" {
					return usagef("--report-file is required")
				}
				var raw []byte
				var err error
				if reportFile == "-" {
					raw, err = io.ReadAll(io.LimitReader(rt.stdin, 1<<20+1))
				} else {
					raw, err = os.ReadFile(reportFile)
				}
				if err != nil {
					return rt.fail(err, "")
				}
				if len(raw) > 1<<20 {
					return usagef("report is too large")
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					return usagef("invalid report JSON: %v", err)
				}
				var result map[string]any
				if err := rt.do(http.MethodPost, "/api/stage-handoffs/"+args[0]+"/result", body, &result); err != nil {
					return err
				}
				return rt.printJSON(result)
			}},
		{Name: "create", Short: "Classic delivery-key handoffs require an Aeon release", Use: "external-stage create <delivery-key>", minArgs: 1, maxArgs: 1, run: func([]string) error {
			return notYet("classic delivery-key handoffs have no Aeon release/approval binding; use external-stage request")
		}},
		{Name: "accept", Short: "Aeon handoffs use server-owned state", Use: "external-stage accept <handoff-id>", minArgs: 1, maxArgs: 1, run: func([]string) error {
			return notYet("Aeon handoff authority is server-owned; acceptance is performed by the bound plugin")
		}},
		{Name: "launch-candidate", Short: "Classic launch admission is not portable", Use: "external-stage launch-candidate <handoff-id>", minArgs: 1, maxArgs: 1, run: func([]string) error {
			return notYet("Aeon uses approved journey gates and first-party plugins for launch admission")
		}},
		{Name: "launch-consume", Short: "Classic launch admission is not portable", Use: "external-stage launch-consume <handoff-id> <admission-id>", minArgs: 2, maxArgs: 2, run: func([]string) error {
			return notYet("Aeon uses approved journey gates and first-party plugins for launch admission")
		}},
		{Name: "credential", Short: "Classic one-time credentials are not portable", Use: "external-stage credential <mint|rotate|revoke>", run: func([]string) error {
			return notYet("Aeon stage plugins use scoped agent keys; no one-time handoff secret exists")
		}},
		{Name: "registrations", Short: "Classic reporter registry is not portable", Use: "external-stage registrations", run: func([]string) error { return notYet("Aeon uses compiled first-party plugin manifests") }},
		{Name: "prerequisites", Short: "Classic prerequisite seals are not portable", Use: "external-stage prerequisites", run: func([]string) error { return notYet("Aeon derives prerequisites from journey and approval state") }},
		{Name: "owner", Short: "Classic reporter owner registry is not portable", Use: "external-stage owner", run: func([]string) error {
			return notYet("Aeon resolves the stage owner from the installed first-party plugin")
		}},
	}}
}

// readDeployTarget accepts only a bounded explicit document. Unknown fields are
// errors so misspelled destination or change fields cannot silently disappear.
func (rt *runtime) readDeployTarget(path string) (*deploytarget.Target, error) {
	var input io.Reader = rt.stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, rt.fail(err, "")
		}
		defer file.Close()
		input = file
	}
	raw, err := io.ReadAll(io.LimitReader(input, (32<<10)+1))
	if err != nil {
		return nil, rt.fail(err, "")
	}
	if len(raw) > 32<<10 {
		return nil, usagef("target is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var target *deploytarget.Target
	if err := decoder.Decode(&target); err != nil {
		return nil, usagef("invalid target JSON: %v", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, usagef("target must contain one JSON object")
	}
	normalized, _, err := deploytarget.Normalize(target)
	if err != nil {
		return nil, usagef("%v", err)
	}
	return normalized, nil
}

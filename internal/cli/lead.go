// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"fmt"
	"net/http"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) cmdLead() *Command {
	return &Command{Name: "lead", Short: "Read or control the explicitly owned project lead", Use: "lead <status|start|claim|pause|handoff>", subs: []*Command{rt.leadCommand("status"), rt.leadCommand("start"), rt.leadCommand("claim"), rt.leadCommand("pause"), rt.leadHandoffCommand()}}
}
func (rt *runtime) leadCommand(action string) *Command {
	var project, session, leaseFile string
	revision, generation := -1, -1
	return &Command{Name: action, Short: map[string]string{"status": "Read lead state", "start": "Request a lead; no process is launched", "claim": "Bind a proven owned coordinator generation", "pause": "Fence dispatch and request cooperative handover"}[action], Use: "lead " + action + " --project KEY", maxArgs: 0, addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key or UUID")
		if action != "status" {
			fs.int(&revision, "expected-revision", "revision shown by lead status")
		}
		if action == "claim" {
			fs.string(&session, "session", 0, "explicit coordinator session UUID")
			fs.string(&leaseFile, "worker-lease-file", 0, "private generation lease file, or - for stdin")
		}
		if action == "pause" {
			fs.int(&generation, "generation", "generation shown by lead status")
			fs.string(&leaseFile, "worker-lease-file", 0, "private lease for worker idle pause; omit for person control")
		}
	}, run: func([]string) error {
		if project == "" {
			return usagef("--project is required")
		}
		if action != "status" && (revision < 0 || action != "start" && revision == 0) {
			return usagef("--expected-revision is required")
		}
		if action == "claim" && !validUUID(session) {
			return usagef("--session must be a UUID")
		}
		if action == "pause" && generation < 0 {
			return usagef("--generation is required")
		}
		var lease string
		var err error
		if action == "claim" || leaseFile != "" {
			lease, err = rt.harnessSecret(leaseFile, "worker-lease-file")
			if err != nil {
				return err
			}
		}
		id, err := rt.harnessProject(project)
		if err != nil {
			return err
		}
		path, method := "/api/projects/"+id+"/lead", http.MethodGet
		var body map[string]any
		if action != "status" {
			method = http.MethodPost
			body = map[string]any{"expected_revision": revision}
		}
		if action == "claim" {
			path += "/claim"
			body["session_id"] = session
		}
		if action == "pause" {
			path += "/pause"
			body["generation"] = generation
		}
		var out harness.Lead
		if err = rt.harnessDo(method, path, lease, body, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		_, err = fmt.Fprintf(rt.stdout, "%s  revision %d · generation %d · process active %t\n", out.State, out.Revision, out.Generation, out.ProcessActive)
		if err == nil && out.Reason != "" {
			_, err = fmt.Fprintln(rt.stdout, out.Reason)
		}
		return err
	}}
}

func (rt *runtime) leadHandoffCommand() *Command {
	return &Command{Name: "handoff", Short: "Inspect a durable worker assignment without launching", Use: "lead handoff <run-UUID>", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		if !validUUID(args[0]) {
			return usagef("run must be a UUID")
		}
		var out agentruns.WorkerHandoff
		if err := rt.do(http.MethodGet, "/api/runs/"+args[0]+"/handoff", nil, &out); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(out)
		}
		_, err := fmt.Fprintf(rt.stdout, "%s  ticket %s · lead generation %d · order revision %d\n", out.State, out.TicketID, out.LeadGeneration, out.WorkOrderRevision)
		return err
	}}
}

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) harnessPause(kind string) *Command {
	var project, session, reason, coordinator, leaseFile, registrationFile, level, note string
	var all bool
	var deadline int
	var except []string
	return &Command{Name: kind, Short: "Request a durable pause or continuation", Use: "harness " + kind + " --project KEY (--session UUID | --all)", addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key")
		fs.string(&session, "session", 0, "public session UUID")
		fs.bool(&all, "all", 0, "all generations you control in this project")
		fs.strings(&except, "except", "excluded session UUID (repeatable with --all)")
		fs.string(&coordinator, "coordinator-session", 0, "proven parent coordinator UUID for agent callers")
		fs.string(&leaseFile, "worker-lease-file", 0, "private coordinator lease file")
		if kind == "pause" {
			fs.string(&reason, "reason", 0, "why the worker should pause")
			fs.string(&level, "level", 0, "stop_now, pause_quickly, pause or wrap_up (uses personal default)")
			fs.string(&note, "note", 0, "optional note for the handover")
			fs.int(&deadline, "deadline-minutes", "handover deadline, default 10 (1-60)")
		} else {
			fs.string(&registrationFile, "registration-file", 0, "fresh private registration JSON; creates the successor for one session")
		}
	}, run: func(args []string) error {
		if len(args) > 0 || all == (session != "") || session != "" && !validUUID(session) {
			return usagef("choose --session UUID or --all")
		}
		if len(except) > 0 && !all {
			return usagef("--except requires --all")
		}
		for _, id := range except {
			if !validUUID(id) {
				return usagef("invalid --except session")
			}
		}
		if all && registrationFile != "" {
			return usagef("--registration-file resumes one session; --all returns durable continuation recipes")
		}
		if coordinator != "" && !validUUID(coordinator) || (coordinator == "") != (leaseFile == "") {
			return usagef("--coordinator-session and --worker-lease-file must be supplied together")
		}
		if level != "" && !harness.ValidPauseLevel(level) {
			return usagef("invalid --level")
		}
		if deadline < 0 || deadline > 60 {
			return usagef("--deadline-minutes must be 1 through 60")
		}
		if !all && project == "" {
			return usagef("--project is required with --session")
		}
		if coordinator != "" && project == "" {
			return usagef("a coordinator must select --project")
		}
		id := ""
		var err error
		if project != "" {
			id, err = rt.harnessProject(project)
			if err != nil {
				return err
			}
		}
		body := map[string]any{}
		lease := ""
		if coordinator != "" {
			lease, err = rt.harnessSecret(leaseFile, "worker-lease-file")
			if err != nil {
				return err
			}
			body["coordinator_session_id"] = coordinator
		}
		if kind == "pause" {
			body["reason"] = reason
			if level != "" {
				body["level"] = level
			}
			if note != "" {
				body["note"] = note
			}
			if deadline != 0 {
				body["deadline_minutes"] = deadline
			}
		}
		if len(except) > 0 {
			body["except"] = except
		}
		if registrationFile != "" {
			ref, key, e := rt.harnessRegistration(registrationFile)
			if e != nil {
				return e
			}
			body["registration"] = map[string]any{"harness_session_ref": ref, "worker_lease": key}
		}
		path := "/api/projects/" + id + "/harness-sessions/" + kind
		if id == "" {
			path = "/api/harness-sessions/" + kind
		}
		if !all {
			path = harnessPath(id, session) + "/" + kind
		}
		var out any
		if err = rt.harnessDo(http.MethodPost, path, lease, body, &out); err != nil {
			return err
		}
		return rt.printHarness(out)
	}}
}

func (rt *runtime) readHandover(path string) (*harness.Handover, error) {
	var reader io.Reader = rt.stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 16001))
	if err != nil {
		return nil, err
	}
	if len(raw) > 16000 {
		return nil, usagef("handover exceeds 16000 bytes")
	}
	var note harness.Handover
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&note); err != nil {
		return nil, usagef("invalid handover JSON")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, usagef("handover must contain one JSON object")
	}
	return &note, nil
}

func (rt *runtime) printHeartbeatPause(sessionID string, pause *harness.Pause, records bool) {
	if pause == nil || pause.StartsAt != nil && !pause.Deliver || !validUUID(pause.ControlID) || (pause.State != "requested" && pause.State != "planned") {
		return
	}
	if records {
		_ = json.NewEncoder(rt.stdout).Encode(struct {
			Type      string         `json:"type"`
			Schema    string         `json:"schema"`
			SessionID string         `json:"session_id"`
			Pause     *harness.Pause `json:"pause"`
		}{"pause_requested", "aeon.harness-pause.v1", sessionID, pause})
		return
	}
	fmt.Fprintf(rt.stderr, "pause requested (%s): control %s; plan a handover, finish or roll back the current step, commit WIP on your branch, then mark-stopped --reason paused --handover-file PATH\n", pause.Level, pause.ControlID)
}

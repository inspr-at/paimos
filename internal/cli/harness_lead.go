// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/harness"
)

func (rt *runtime) harnessLead() *Command {
	var project, stateDir, refFile, leaseFile string
	revision := -1
	claim := &Command{Name: "claim", Short: "Prove the running session selected by its person", Use: "harness lead claim --state-dir DIR [--project KEY] | --project KEY --harness-session-file PATH --worker-lease-file PATH", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&stateDir, "state-dir", 0, "existing run-heartbeat state directory; derives the registered reference, lease and project")
			fs.string(&project, "project", 'p', "project key or UUID; defaults to the state directory's project")
			fs.string(&refFile, "harness-session-file", 0, "private raw registered reference: HARNESS:SOURCE-UUID for a coordinator with --source-session, otherwise session.ref")
			fs.string(&leaseFile, "worker-lease-file", 0, "private existing worker lease file")
			fs.int(&revision, "expected-revision", "lead revision; defaults to a fresh read")
		}, run: func([]string) error {
			if stateDir != "" && (refFile != "" || leaseFile != "") {
				return usagef("--state-dir cannot be combined with raw reference or lease files")
			}
			if refFile == "-" && leaseFile == "-" {
				return usagef("the session reference and lease cannot both read stdin")
			}
			var ref, lease, boundProject string
			var err error
			if stateDir != "" {
				ref, lease, boundProject, err = rt.harnessLeadStateProof(stateDir)
				if err != nil {
					return rt.fail(err, ref, lease)
				}
			} else {
				ref, err = rt.harnessSecret(refFile, "harness-session-file")
				if err != nil {
					return usagef("private harness session file unavailable or invalid; use --state-dir or private raw proof files")
				}
				lease, err = rt.harnessSecret(leaseFile, "worker-lease-file")
				if err != nil {
					return usagef("private worker lease file unavailable or invalid")
				}
			}
			if len(ref) < 16 || len(ref) > 4096 || strings.ContainsAny(ref, "\r\n") || len(lease) < 32 || len(lease) > 256 {
				return usagef("invalid session reference or worker lease")
			}
			id := boundProject
			if project != "" || id == "" {
				id, err = rt.harnessProject(project)
				if err != nil {
					return rt.fail(err, ref, lease)
				}
			}
			if boundProject != "" && !strings.EqualFold(id, boundProject) {
				return usagef("--project differs from the state directory's project")
			}
			path := "/api/projects/" + id + "/lead"
			if revision < 0 {
				var current harness.Lead
				if err = rt.harnessDo(http.MethodGet, path, "", nil, &current); err != nil {
					return rt.fail(err, ref, lease)
				}
				revision = int(current.Revision)
			}
			if revision < 1 {
				return usagef("a person must first confirm Adopt a running session on the project lead card")
			}
			var out harness.Lead
			if err = rt.harnessDo(http.MethodPost, path+"/claim", lease, map[string]any{"expected_revision": revision, "harness_session_ref": ref}, &out); err != nil {
				return rt.fail(err, ref, lease)
			}
			// Even a malformed/echoing upstream cannot expose the proof in success output.
			text := out.ProjectID + out.State + out.Reason
			if out.SessionID != nil {
				text += *out.SessionID
			}
			if strings.Contains(text, ref) || strings.Contains(text, lease) {
				return rt.fail(fmt.Errorf("lead response contained private proof"), ref, lease)
			}
			if rt.jsonOut {
				return rt.printJSON(out)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s · revision %d · generation %d\n", out.State, out.Revision, out.Generation)
			return err
		}}
	return &Command{Name: "lead", Short: "Claim the explicitly selected project lead", Use: "harness lead claim", subs: []*Command{claim}}
}

// Claim reads a running helper's private state without taking its lifetime lock
// or writing any files. The final server mutation still checks the exact lease.
func (rt *runtime) harnessLeadStateProof(path string) (ref, lease, project string, err error) {
	invalid := usagef("private heartbeat state unavailable or invalid")
	if _, statErr := os.Lstat(path); statErr != nil {
		return "", "", "", invalid
	}
	dir, openErr := openPrivateHeartbeatDir(path)
	if openErr != nil {
		return "", "", "", invalid
	}
	hold := heartbeatHold{dir: dir}
	defer hold.release()
	session, ok, loadErr := loadHeartbeatSession(&hold)
	if loadErr != nil || !ok || session.disk.Closed || session.disk.Terminal || !validUUID(session.disk.ProjectID) {
		return "", "", "", invalid
	}
	lease, project = session.lease, session.disk.ProjectID
	ref = session.disk.RegisteredRef
	if ref != "" {
		return ref, lease, project, nil
	}
	// Older helpers kept only the random fallback in session.ref. A published
	// source binding identifies the native session; the server supplies its
	// registered role and harness, rather than guessing from usage log paths.
	ref, err = readStateSecret(&hold, "session.ref")
	if err != nil {
		return ref, lease, project, invalid
	}
	raw, readErr := hold.readFile("index.source", 256)
	if errors.Is(readErr, os.ErrNotExist) {
		return ref, lease, project, nil
	}
	source := strings.TrimSpace(string(raw))
	if readErr != nil || !validUUID(source) {
		return ref, lease, project, invalid
	}
	var registered struct {
		ID      string `json:"id"`
		Harness string `json:"harness"`
		Role    string `json:"role"`
	}
	if err = rt.harnessDo(http.MethodGet, harnessPath(project, session.id), "", nil, &registered); err != nil {
		return ref, lease, project, fmt.Errorf("registered session metadata unavailable")
	}
	if !strings.EqualFold(registered.ID, session.id) || registered.Harness == "" || (registered.Role != "coordinator" && registered.Role != "worker") {
		return ref, lease, project, invalid
	}
	ref = heartbeatRegistrationRef(heartbeatOptions{Harness: registered.Harness, Role: registered.Role, SourceSession: source}, ref)
	return ref, lease, project, nil
}

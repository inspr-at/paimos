// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/version"
)

type subagentRequest struct {
	Parent  string `json:"parent"`
	Project string `json:"project"`
	Label   string `json:"label"`
	Model   string `json:"model,omitempty"`
	// Persist the exact public registration metadata before the request, so a
	// lost response replays with the same ref, lease and metadata.
	Registration map[string]any `json:"registration,omitempty"`
}

// Only an explicit start publishes this authority. Maintenance consumes it
// under the child lock, including when the start hook found that lock busy.
type subagentStart struct {
	Request    subagentRequest `json:"request"`
	ResumeFrom string          `json:"resume_from,omitempty"`
}

func subagentHookEvent(event string) bool {
	return event == "SubagentStart" || event == "SubagentStop"
}

func subagentDirName(name string) bool {
	if len(name) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil && name == strings.ToLower(name)
}

func subagentName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func (rt *runtime) runSubagentHook(ctx context.Context, event string) error {
	// Stop envelopes include the final response. Match the inbox envelope's
	// bounded size, but decode only lifecycle metadata; response text, tool
	// content and transcript paths must never enter persisted child state.
	var input struct {
		Event       string  `json:"hook_event_name"`
		Session     string  `json:"session_id"`
		AgentID     *string `json:"agent_id"`
		AgentType   string  `json:"agent_type"`
		Description string  `json:"description"`
		Model       string  `json:"model"`
	}
	raw, err := io.ReadAll(io.LimitReader(rt.stdin, 16<<20+1))
	if err != nil || len(raw) > 16<<20 || json.Unmarshal(raw, &input) != nil || input.Event != event || input.AgentID == nil || len(*input.AgentID) == 0 || len(*input.AgentID) > 128 || strings.ContainsAny(*input.AgentID, "\r\n\x00") {
		return errors.New("invalid subagent hook input")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Lifecycle registration requires the private index, even when an inbox
	// env binding is present. A payload never supplies the project or ticket.
	id, _, stateDir, result := lookupSessionIndexState(input.Session)
	if result != sessionIndexBound {
		return nil
	}
	dir, err := openSubagentParent(stateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	parentHold := heartbeatHold{dir: dir}
	state, err := parentHold.readFile("state.json", 1<<20)
	var parent heartbeatDisk
	if err != nil || len(state) > 1<<20 || json.Unmarshal(state, &parent) != nil || parent.Schema != heartbeatSchema || parent.SessionID != id || !validUUID(parent.ProjectID) || parent.Closed || parent.Terminal {
		return nil
	}
	children, err := openSubagentDir(dir, "subagents", true)
	if err != nil {
		return err
	}
	defer children.Close()
	childDir, err := openSubagentDir(children, subagentName(*input.AgentID), true)
	if err != nil {
		return err
	}
	defer childDir.Close()
	label := heartbeatText(input.AgentType, 128)
	if label == "" {
		label = "Claude subagent"
	}
	if description := heartbeatText(input.Description, 128); description != "" {
		if combined := heartbeatText(label+": "+description, 128); combined != "" {
			label = combined
		}
	}
	request := subagentRequest{Parent: id, Project: parent.ProjectID, Label: label, Model: heartbeatText(input.Model, 128)}
	if err := publishSubagentHook(ctx, childDir, event, request); err != nil {
		return err
	}
	hold, err := lockSubagentDir(childDir)
	if errors.Is(err, errHeartbeatBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer hold.lock.Close()
	if rt.subagentLocked != nil {
		rt.subagentLocked()
	}
	req, err := readSubagentWork(&hold)
	if errors.Is(err, os.ErrNotExist) && event == "SubagentStop" {
		return nil
	}
	if err != nil {
		return err
	}
	if req.Parent != id || req.Project != parent.ProjectID {
		return errHeartbeatState
	}
	err = rt.syncSubagent(ctx, &hold, &req, false)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func publishSubagentHook(ctx context.Context, dir *os.File, event string, req subagentRequest) error {
	lock, err := lockSubagentLifecycle(ctx, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	hold := heartbeatHold{dir: dir}
	if event == "SubagentStop" {
		// The short lifecycle lock never waits for registration or heartbeat
		// I/O. Stops can still land while either holds the child lock.
		return hold.writeFile("subagent.stop", []byte(req.Parent+"\n"))
	}
	pending, err := readSubagentStart(&hold)
	if err == nil {
		if pending.Request.Parent != req.Parent || pending.Request.Project != req.Project {
			return errHeartbeatState
		}
		// A repeated start must not clear a stop for an already pending resume.
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, err := readSubagentRequest(&hold)
	start := subagentStart{Request: req}
	if err == nil {
		if current.Parent != req.Parent || current.Project != req.Project {
			return errHeartbeatState
		}
		// Read only public state here; the child lock owns its proof and I/O.
		raw, err := hold.readFile("state.json", 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			return nil // Existing registration is still in flight.
		}
		var disk heartbeatDisk
		if err != nil {
			return err
		}
		if json.Unmarshal(raw, &disk) != nil || disk.Schema != heartbeatSchema || !validUUID(disk.SessionID) || disk.ProjectID != req.Project {
			return errHeartbeatState
		}
		if !disk.Closed || disk.Terminal {
			return nil
		}
		start.ResumeFrom = disk.SessionID
		// Clear only the completed generation's stop, serialized with every
		// stop publication. Reset never unlinks a subsequently published stop.
		if err := hold.remove("subagent.stop"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.Marshal(start)
	if err != nil || len(raw) > 16<<10 {
		return errHeartbeatState
	}
	return hold.writeFile("subagent.start", raw)
}

func readSubagentStart(hold *heartbeatHold) (subagentStart, error) {
	raw, err := hold.readFile("subagent.start", 16<<10)
	var start subagentStart
	if err != nil {
		return start, err
	}
	if json.Unmarshal(raw, &start) != nil || !validUUID(start.Request.Parent) || !validUUID(start.Request.Project) || heartbeatText(start.Request.Label, 128) == "" || start.Request.Registration != nil || (start.ResumeFrom != "" && !validUUID(start.ResumeFrom)) {
		return start, errHeartbeatState
	}
	return start, nil
}

func readSubagentWork(hold *heartbeatHold) (subagentRequest, error) {
	req, err := readSubagentRequest(hold)
	if errors.Is(err, os.ErrNotExist) {
		start, err := readSubagentStart(hold)
		return start.Request, err
	}
	return req, err
}

func prepareSubagent(ctx context.Context, hold *heartbeatHold, req *subagentRequest, closing bool) (heartbeatSession, bool, error) {
	lock, err := lockSubagentLifecycle(ctx, hold.dir)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	defer lock.Close()
	session, exists, err := loadHeartbeatSession(hold)
	if err != nil || closing {
		return session, exists, err
	}
	start, err := readSubagentStart(hold)
	if errors.Is(err, os.ErrNotExist) {
		return session, exists, nil
	}
	if err != nil {
		return session, exists, err
	}
	if start.Request.Parent != req.Parent || start.Request.Project != req.Project {
		return session, exists, errHeartbeatState
	}
	if exists && session.disk.Closed && !session.disk.Terminal && start.ResumeFrom == session.id {
		if err := clearHeartbeatIdentity(hold); err != nil {
			return session, exists, err
		}
		exists = false
	}
	if !exists {
		*req = start.Request
		if err := writeSubagentRequest(hold, *req); err != nil {
			return session, exists, err
		}
	}
	return session, exists, hold.remove("subagent.start")
}

func readSubagentRequest(hold *heartbeatHold) (subagentRequest, error) {
	raw, err := hold.readFile("subagent.json", 16<<10)
	var req subagentRequest
	if err != nil {
		return req, err
	}
	if json.Unmarshal(raw, &req) != nil || !validUUID(req.Parent) || !validUUID(req.Project) || heartbeatText(req.Label, 128) == "" {
		return req, errHeartbeatState
	}
	return req, nil
}

func writeSubagentRequest(hold *heartbeatHold, req subagentRequest) error {
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > 16<<10 {
		return errHeartbeatState
	}
	return hold.writeFile("subagent.json", raw)
}

func (rt *runtime) syncSubagent(ctx context.Context, hold *heartbeatHold, req *subagentRequest, closing bool) error {
	session, exists, err := prepareSubagent(ctx, hold, req, closing)
	if err != nil {
		return err
	}
	if exists && (session.disk.Closed || session.disk.Terminal) {
		return nil
	}
	if !exists {
		if closing {
			return nil
		}
		if req.Registration == nil {
			var parent harness.Session
			if err := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(req.Project, req.Parent), "", nil, &parent); err != nil {
				return err
			}
			if parent.ID != req.Parent || parent.ProjectID != req.Project || parent.StoppedAt != nil || parent.ArchivedAt != nil {
				return nil
			}
			if !validUUID(parent.AgentPrincipalID) || heartbeatText(parent.Host, 200) == "" {
				return errHeartbeatState
			}
			body := map[string]any{
				"agent_principal_id": parent.AgentPrincipalID, "harness": "claude", "host": parent.Host,
				"management_mode": "unmanaged", "role": "worker", "parent_harness_session_id": req.Parent,
				"display_label": req.Label, "advertised_capabilities": []string{"status"},
				"max_session_file_bytes": rules.SessionFileLimit("claude"), "rules_client_version": version.Version,
			}
			if parent.TicketNodeID != nil {
				body["ticket_node_id"] = *parent.TicketNodeID
				shape := "scout"
				if parent.WorkShape == "ship" {
					shape = "ship"
				}
				body["work_shape"] = shape
			}
			putText(body, "model", req.Model, true)
			req.Registration = body
			if err := writeSubagentRequest(hold, *req); err != nil {
				return err
			}
		}
		lease, err := readOrCreateStateSecret(hold, "lease.key", 32)
		if err != nil {
			return err
		}
		ref, err := readOrCreateStateSecret(hold, "session.ref", 24)
		if err != nil {
			return err
		}
		body := make(map[string]any, len(req.Registration)+2)
		for key, value := range req.Registration {
			body[key] = value
		}
		body["worker_lease"], body["harness_session_ref"] = lease, ref
		var out struct {
			ID string `json:"id"`
		}
		if err := rt.harnessDoCtx(ctx, http.MethodPost, harnessPath(req.Project, ""), "", body, &out); err != nil {
			return err
		}
		if !validUUID(out.ID) {
			return errHeartbeatState
		}
		session = heartbeatSession{id: out.ID, lease: lease, hold: *hold, disk: heartbeatDisk{Schema: heartbeatSchema, SessionID: out.ID, ProjectID: req.Project, SentLabel: req.Label}}
		if err := saveHeartbeatSession(&session); err != nil {
			return err
		}
	}
	if session.disk.ProjectID != req.Project {
		return errHeartbeatState
	}
	stop, err := hold.readFile("subagent.stop", 256)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if closing || (err == nil && strings.TrimSpace(string(stop)) == req.Parent) {
		// Strict stop does not mistake rejected proof for a successful stop.
		err := rt.stopHeartbeatStrict(ctx, "", session)
		if err != nil && !errors.Is(err, errHeartbeatAlreadyStopped) {
			return err
		}
		session.disk.Closed = true
		return saveHeartbeatSession(&session)
	}
	session.disk.Sequence++
	if err := saveHeartbeatSession(&session); err != nil {
		return err
	}
	err = rt.harnessDoCtx(ctx, http.MethodPost, harnessPath(req.Project, session.id)+"/heartbeat", session.lease,
		map[string]any{"activity_sequence": session.disk.Sequence, "phase": "working", "activity": "busy", "max_session_file_bytes": rules.SessionFileLimit("claude"), "rules_client_version": version.Version}, new(any))
	if heartbeatTerminalStatus(err) {
		markHeartbeatTerminal(&session, terminalReason(err))
		if saveErr := saveHeartbeatSession(&session); saveErr != nil {
			return saveErr
		}
	}
	return err
}

// The existing parent helper maintains children without launching a watcher,
// granting inbox access, or reading the parent's lease. The directory cursor
// bounds work even after many completed children accumulate.
func (rt *runtime) heartbeatSubagents(ctx context.Context, parent *heartbeatSession, closing bool) {
	if parent == nil || parent.hold.dir == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if parent.subagentScan == nil {
		dir, err := openSubagentDir(parent.hold.dir, "subagents", false)
		if err != nil {
			return
		}
		parent.subagentScan = dir
	}
	for examined := 0; examined < 512 && ctx.Err() == nil; examined++ {
		if len(parent.subagentNames) == 0 {
			parent.subagentNames, _ = parent.subagentScan.Readdirnames(64)
			if len(parent.subagentNames) == 0 {
				parent.subagentScan.Close()
				parent.subagentScan = nil
				return
			}
		}
		name := parent.subagentNames[0]
		parent.subagentNames = parent.subagentNames[1:]
		if !subagentDirName(name) {
			continue
		}
		dir, err := openSubagentDir(parent.subagentScan, name, false)
		if err != nil {
			continue
		}
		hold, err := lockSubagentDir(dir)
		if err == nil {
			if rt.subagentLocked != nil {
				rt.subagentLocked()
			}
			req, readErr := readSubagentWork(&hold)
			if readErr == nil && req.Parent == parent.id && req.Project == parent.disk.ProjectID {
				childCtx, childCancel := context.WithTimeout(ctx, hookBudget)
				if rt.syncSubagent(childCtx, &hold, &req, closing) != nil {
					// Content-free: metadata, leases and server errors stay private.
					// The persisted request is retried on a later parent heartbeat.
					fmt.Fprintln(rt.stderr, "heartbeat: subagent update incomplete; will retry")
				}
				childCancel()
			}
			hold.lock.Close()
		}
		dir.Close()
	}
}

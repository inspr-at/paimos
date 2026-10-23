// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/inbox"
)

type laneStatusFlags struct {
	kind, target, done, blocker, owner, next, eta string
}

func (f *laneStatusFlags) addFlags(fs *flagSet) {
	fs.string(&f.kind, "kind", 0, "status for structured lane progress; omit for ordinary messages")
	fs.string(&f.target, "target", 0, "goal or ticket key/UUID for a status report")
	fs.string(&f.done, "done", 0, "done since the last status")
	fs.string(&f.blocker, "blocker", 0, "current blocker; requires --blocker-owner")
	fs.string(&f.owner, "blocker-owner", 0, "who can resolve the blocker")
	fs.string(&f.next, "next", 0, "next step")
	fs.string(&f.eta, "eta", 0, "RFC3339 completion estimate, or unknown")
}

func (f laneStatusFlags) present() bool {
	return f.kind != "" || f.target != "" || f.done != "" || f.blocker != "" || f.owner != "" || f.next != "" || f.eta != ""
}

func (rt *runtime) laneTarget(raw string) (string, error) {
	if validUUID(raw) {
		return strings.ToLower(raw), nil
	}
	if raw == "" || len(raw) > 128 {
		return "", usagef("--target requires a goal or ticket key/UUID")
	}
	var node apiNode
	if err := rt.do(http.MethodGet, "/api/node-keys/"+url.PathEscape(raw), nil, &node); err != nil {
		return "", err
	}
	if !validUUID(node.ID) {
		return "", rt.fail(fmt.Errorf("target lookup returned an invalid identity"), "")
	}
	return node.ID, nil
}

func (rt *runtime) tellLaneStatus(address, project, senderSession, recipientSession, key string, principalInbox bool, f laneStatusFlags) error {
	if f.kind != "status" || f.target == "" || f.eta == "" {
		return usagef("structured status requires --kind status, --target, --done, --next and --eta (RFC3339 or unknown)")
	}
	if !validUUID(address) && (principalInbox || !messagingAddressRE.MatchString(address)) {
		return usagef("status recipient must be a principal UUID or a project harness:agent address")
	}
	if senderSession != "" && !validUUID(senderSession) || recipientSession != "" && !validUUID(recipientSession) {
		return usagef("session IDs must be UUIDs")
	}
	if len(key) > 128 || strings.ContainsRune(key, 0) {
		return usagef("invalid --idempotency-key")
	}
	if strings.TrimSpace(f.done) == "" || strings.TrimSpace(f.next) == "" || (strings.TrimSpace(f.blocker) == "") != (strings.TrimSpace(f.owner) == "") {
		return usagef("status requires --done, --next and an owner for each blocker")
	}
	status := inbox.LaneStatus{Done: f.done, Blocker: f.blocker, BlockerOwner: f.owner, Next: f.next}
	if f.eta != "unknown" {
		eta, err := time.Parse(time.RFC3339, f.eta)
		if err != nil {
			return usagef("--eta must be RFC3339 or unknown")
		}
		status.ETA = &eta
	}
	path := "/api/inbox/messages"
	if principalInbox {
		if project != "" || senderSession == "" {
			return usagef("inbox status requires --sender-session and cannot use --project")
		}
	} else {
		if project == "" {
			return usagef("--project is required unless --inbox")
		}
		projectID, err := rt.projectIdentityCtx(rt.context(), project)
		if err != nil {
			return err
		}
		if senderSession == "" {
			me, err := rt.caller()
			if err != nil {
				return err
			}
			senderSession, err = rt.ambientSenderSession(rt.context(), projectID, me.Principal.ID)
			if err != nil {
				return rt.fail(err, "")
			}
		}
		if senderSession == "" {
			return usagef("status requires an exact --sender-session or current Aeon session binding")
		}
		path = "/api/projects/" + url.PathEscape(projectID) + "/messages"
	}
	var err error
	status.TargetNodeID, err = rt.laneTarget(f.target)
	if err != nil {
		return err
	}
	if err := status.Validate(); err != nil {
		return usagef("%s", err)
	}
	if key == "" {
		key, err = newUUIDv4()
		if err != nil {
			return err
		}
	}
	req := map[string]any{"kind": "status", "lane_status": status, "body": status.Line(), "sender_session_id": senderSession, "idempotency_key": key}
	if principalInbox {
		req["recipient_principal_id"] = address
	} else {
		req["to"] = address
	}
	if recipientSession != "" {
		req["recipient_session_id"] = recipientSession
	}
	var sent map[string]any
	if err := rt.do(http.MethodPost, path, req, &sent); err != nil {
		return err
	}
	id, _ := sent["id"].(string)
	if !validUUID(id) {
		return rt.fail(fmt.Errorf("status response has no message UUID"), "")
	}
	receipt, receiptErr := rt.tellReceipt(id)
	state, push := "unavailable", "receipt unavailable"
	if receiptErr == nil {
		push = tellReceiptState(receipt)
		if receipt.State == "queued" || receipt.State == "handed_off" || receipt.State == "failed" {
			state = receipt.State
		}
	}
	if rt.jsonOut {
		sent["stored"], sent["receipt_state"] = true, state
		return rt.printJSON(sent)
	}
	_, err = fmt.Fprintf(rt.stdout, "stored status: %s\n%s\npush: %s\nmessage: %s\n", status.TargetNodeID, status.Line(), push, id)
	return err
}

func (rt *runtime) cmdLanes() *Command {
	cmd := rt.laneQueryCommand("lanes", 0)
	cmd.subs = []*Command{rt.laneQueryCommand("silent", 30)}
	return cmd
}

func (rt *runtime) laneQueryCommand(name string, defaultMinutes int) *Command {
	var target, session, after string
	limit, minutes := 50, defaultMinutes
	return &Command{Name: name, Short: "Latest lane status and silence", Use: name + " [--target KEY|UUID] [--silent-minutes N] [--limit N] [--after CURSOR]", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&target, "target", 0, "goal or ticket key/UUID")
			fs.string(&session, "session", 0, "exact lane session UUID")
			fs.string(&after, "after", 0, "next_after from the preceding page")
			fs.int(&limit, "limit", "page size (1–100)")
			fs.int(&minutes, "silent-minutes", "only lanes silent longer than N minutes")
			if name == "silent" {
				fs.int(&minutes, "minutes", "silence threshold in minutes (default 30)")
			}
		}, run: func(args []string) error {
			if limit < 1 || limit > 100 || minutes < 0 || minutes > 525600 || name == "silent" && minutes == 0 {
				return usagef("limit must be 1–100 and silence 1–525600 minutes")
			}
			if session != "" && !validUUID(session) {
				return usagef("--session must be a UUID")
			}
			if after != "" {
				if len(after) != 73 {
					return usagef("invalid --after cursor")
				}
				parts := strings.Split(after, "/")
				if len(parts) != 2 || !validUUID(parts[0]) || !validUUID(parts[1]) {
					return usagef("invalid --after cursor")
				}
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			if target != "" {
				id, err := rt.laneTarget(target)
				if err != nil {
					return err
				}
				q.Set("target", id)
			}
			if session != "" {
				q.Set("session", session)
			}
			if after != "" {
				q.Set("after", after)
			}
			if minutes > 0 {
				q.Set("silent_minutes", strconv.Itoa(minutes))
			}
			var page inbox.LanePage
			if err := rt.do(http.MethodGet, "/api/inbox/lanes?"+q.Encode(), nil, &page); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(page)
			}
			for _, lane := range page.Items {
				line := lane.Line
				if lane.Status == nil {
					line = "no status reported"
				}
				if _, err := fmt.Fprintf(rt.stdout, "%s  session=%s target=%s silent=%ds\n%s\n", lane.Label, lane.SessionID, lane.TargetNodeID, lane.SilentSeconds, line); err != nil {
					return err
				}
			}
			if page.NextAfter != "" {
				_, err := fmt.Fprintf(rt.stdout, "next_after: %s\n", page.NextAfter)
				return err
			}
			return nil
		}}
}

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/assistants"
)

func (rt *runtime) cmdAssistant() *Command {
	root := &Command{Name: "assistant", Short: "Hosted assistant identities and policies", Use: "assistant <list|show|create|update|disable|policy>",
		Long: "Writes require an existing person session (--session-cookie-file). Policies do not grant keys, roles or project access. Existing messaging credentials stay on agents assistant create|rotate|revoke.",
		subs: []*Command{rt.cmdAssistantList(), rt.cmdAssistantShow(), rt.cmdAssistantCreate(), rt.cmdAssistantUpdate(), rt.cmdAssistantDisable(), rt.cmdAssistantPolicy()}}
	rt.assistantSessions(root)
	return root
}

func (rt *runtime) assistantSessions(cmd *Command) {
	if cmd.run == nil {
		for _, child := range cmd.subs {
			rt.assistantSessions(child)
		}
		return
	}
	var sessionFile string
	flags, run := cmd.addFlags, cmd.run
	cmd.addFlags = func(fs *flagSet) {
		if flags != nil {
			flags(fs)
		}
		fs.string(&sessionFile, "session-cookie-file", 0, "existing person session; uses cookie authentication only")
	}
	cmd.run = func(args []string) error {
		if sessionFile != "" {
			if rt.agentName != "" {
				return usagef("--session-cookie-file cannot use agent attribution")
			}
			if err := rt.usePersonSession(sessionFile); err != nil {
				return err
			}
		}
		return run(args)
	}
}

func (rt *runtime) assistantRequest(method, path string, body any) error {
	var out json.RawMessage
	var headers map[string]string
	if rt.personClient != nil {
		headers = map[string]string{"Origin": rt.personClient.BaseURL}
	}
	if err := rt.doHeaders(method, path, body, &out, headers); err != nil {
		return err
	}
	return rt.printJSON(out)
}

func (rt *runtime) cmdAssistantList() *Command {
	var after, limit string
	return &Command{Name: "list", Short: "List a bounded page of assistants", Use: "assistant list [--limit 1..100] [--after UUID] [--json]", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&after, "after", 0, "next_cursor from the preceding page")
			fs.string(&limit, "limit", 0, "page size, 1–100 (default 50)")
		}, run: func([]string) error {
			q := url.Values{}
			if after != "" {
				if !validUUID(after) {
					return usagef("--after must be a UUID")
				}
				q.Set("after", after)
			}
			if limit != "" {
				n, err := strconv.Atoi(limit)
				if err != nil || n < 1 || n > 100 {
					return usagef("--limit must be 1–100")
				}
				q.Set("limit", limit)
			}
			path := "/api/assistants"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			return rt.assistantRequest("GET", path, nil)
		}}
}

func (rt *runtime) cmdAssistantShow() *Command {
	return &Command{Name: "show", Short: "Read an assistant identity and policy", Use: "assistant show PRINCIPAL-ID [--json]", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		if !validUUID(args[0]) {
			return usagef("principal-id must be a UUID")
		}
		return rt.assistantRequest("GET", "/api/assistants/"+args[0], nil)
	}}
}

func (rt *runtime) assistantPolicyFile(file string) (assistants.Policy, error) {
	var out assistants.Policy
	if file == "" {
		return out, usagef("--policy-file is required (JSON file or - for stdin)")
	}
	var raw []byte
	var err error
	if file == "-" {
		raw, err = readBounded(rt.stdin, 32768)
	} else {
		raw, err = readBoundedFile(file, 32768)
	}
	if err != nil {
		return out, err
	}
	if len(raw) > 32768 {
		return out, usagef("policy exceeds 32768 bytes")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF {
		return out, usagef("policy must be one AssistantPolicy JSON object")
	}
	return out, nil
}

func (rt *runtime) cmdAssistantCreate() *Command {
	var name, vendor, owner, existing, file string
	return &Command{Name: "create", Short: "Create or link a hosted assistant without issuing credentials", Use: "assistant create --name NAME --vendor grok --owner UUID --policy-file PATH|- [--principal UUID]", maxArgs: 0,
		addFlags: func(fs *flagSet) {
			fs.string(&name, "name", 0, "assistant display name")
			fs.string(&vendor, "vendor", 0, "grok or unknown")
			fs.string(&owner, "owner", 0, "person owner UUID")
			fs.string(&existing, "principal", 0, "existing messaging-only principal UUID to link")
			fs.string(&file, "policy-file", 0, "AssistantPolicy JSON file, or - for stdin")
		}, run: func([]string) error {
			if strings.TrimSpace(name) == "" || !validUUID(owner) || existing != "" && !validUUID(existing) || vendor != "grok" && vendor != "unknown" {
				return usagef("--name, --owner UUID and --vendor grok|unknown are required; --principal must be a UUID")
			}
			policy, err := rt.assistantPolicyFile(file)
			if err != nil {
				return err
			}
			body := map[string]any{"display_name": strings.TrimSpace(name), "vendor": vendor, "owner_person_id": owner, "policy": policy}
			if existing != "" {
				body["principal_id"] = existing
			}
			return rt.assistantRequest("POST", "/api/assistants", body)
		}}
}

func assistantRevision(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, usagef("--expected-revision is required and must be a non-negative integer")
	}
	return n, nil
}

func (rt *runtime) cmdAssistantUpdate() *Command {
	var name, vendor, revision string
	return &Command{Name: "update", Short: "Update metadata at a known revision", Use: "assistant update PRINCIPAL-ID --expected-revision N [--name NAME] [--vendor grok|unknown]", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&name, "name", 0, "new display name")
			fs.string(&vendor, "vendor", 0, "grok or unknown")
			fs.string(&revision, "expected-revision", 0, "revision returned by show")
		}, run: func(args []string) error {
			if !validUUID(args[0]) || strings.TrimSpace(name) == "" && vendor == "" || vendor != "" && vendor != "grok" && vendor != "unknown" {
				return usagef("principal-id must be a UUID and --name or --vendor grok|unknown is required")
			}
			n, err := assistantRevision(revision)
			if err != nil {
				return err
			}
			body := map[string]any{"expected_revision": n}
			if name != "" {
				body["display_name"] = strings.TrimSpace(name)
			}
			if vendor != "" {
				body["vendor"] = vendor
			}
			return rt.assistantRequest("PATCH", "/api/assistants/"+args[0], body)
		}}
}

func (rt *runtime) cmdAssistantDisable() *Command {
	return &Command{Name: "disable", Short: "Deactivate an assistant and revoke its keys", Use: "assistant disable PRINCIPAL-ID", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		if !validUUID(args[0]) {
			return usagef("principal-id must be a UUID")
		}
		return rt.assistantRequest("POST", "/api/assistants/"+args[0]+"/disable", nil)
	}}
}

func (rt *runtime) cmdAssistantPolicy() *Command {
	var file, revision string
	show := &Command{Name: "show", Short: "Read a policy (an assistant may read its own)", Use: "assistant policy show PRINCIPAL-ID", minArgs: 1, maxArgs: 1, run: func(args []string) error {
		if !validUUID(args[0]) {
			return usagef("principal-id must be a UUID")
		}
		return rt.assistantRequest("GET", "/api/assistants/"+args[0]+"/policy", nil)
	}}
	set := &Command{Name: "set", Short: "Replace a policy at a known revision", Use: "assistant policy set PRINCIPAL-ID --expected-revision N --policy-file PATH|-", minArgs: 1, maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&file, "policy-file", 0, "AssistantPolicy JSON file, or - for stdin")
			fs.string(&revision, "expected-revision", 0, "revision returned by show")
		}, run: func(args []string) error {
			if !validUUID(args[0]) {
				return usagef("principal-id must be a UUID")
			}
			n, err := assistantRevision(revision)
			if err != nil {
				return err
			}
			policy, err := rt.assistantPolicyFile(file)
			if err != nil {
				return err
			}
			return rt.assistantRequest("PUT", "/api/assistants/"+args[0]+"/policy", map[string]any{"expected_revision": n, "policy": policy})
		}}
	return &Command{Name: "policy", Short: "Read or replace a per-assistant policy", Use: "assistant policy <show|set>", subs: []*Command{show, set}}
}

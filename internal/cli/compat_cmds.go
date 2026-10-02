// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

var (
	modelRoles = map[string]bool{
		"scout": true, "mechanical": true, "build": true, "build-hard": true, "review-gate": true,
	}
	modelHarnesses = map[string]bool{
		"codex": true, "claude": true, "pi": true, "cursor": true, "grok": true, "gemini": true, "opencode": true,
	}
	agentNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

type modelProfile struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Version string `json:"version"`
	Family  string `json:"family"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

type modelCandidate struct {
	ProfileID   string   `json:"profile_id"`
	Selected    bool     `json:"selected"`
	SkipReasons []string `json:"skip_reasons"`
}

type modelResolution struct {
	Role            string                         `json:"role"`
	AuthorFamily    string                         `json:"author_family"`
	Profile         *modelProfile                  `json:"profile"`
	Ladder          []modelCandidate               `json:"ladder"`
	CommandTemplate string                         `json:"command_template"`
	OwnerRequired   bool                           `json:"owner_required"`
	Source          string                         `json:"source"`
	Preference      *modelregistry.PreferenceTrace `json:"preference,omitempty"`
}

func (rt *runtime) resolveModel(role, author, harness string) error {
	return rt.resolveModelForTicket(role, author, harness, "")
}
func (rt *runtime) resolveModelForTicket(role, author, harness, ticket string) error {
	role = strings.TrimSpace(role)
	if !modelRoles[role] && !(role == "" && ticket != "") {
		return usagef("unknown model role %q", role)
	}
	author, err := modelregistry.NormalizeAuthorFamily(author)
	if err != nil {
		return usagef("%s", err)
	}
	if role == "review-gate" && author == "" {
		return usagef("review-gate requires --author-family")
	}
	harness = strings.TrimSpace(harness)
	if harness != "" && !modelHarnesses[harness] {
		return usagef("unsupported harness %q", harness)
	}
	q := url.Values{}
	if role != "" {
		q.Set("role", role)
	}
	if ticket != "" {
		q.Set("ticket", ticket)
	}
	if author != "" {
		q.Set("author_family", author)
	}
	if harness != "" {
		q.Set("harness", harness)
	}
	var result modelResolution
	if err := rt.do(http.MethodGet, "/api/models/resolve?"+q.Encode(), nil, &result); err != nil {
		return err
	}
	if result.Role == "review-gate" && result.Profile != nil && result.Profile.Family == author {
		return rt.fail(fmt.Errorf("invalid review resolution: selected author family"), "")
	}
	var profiles []modelProfile
	if err := rt.do(http.MethodGet, "/api/models", nil, &profiles); err != nil {
		return err
	}
	slugByID := map[string]string{}
	for _, p := range profiles {
		slugByID[p.ID] = p.Slug
	}
	if rt.jsonOut {
		ladder := make([]map[string]any, 0, len(result.Ladder))
		for _, step := range result.Ladder {
			ladder = append(ladder, map[string]any{
				"profile_id":   slugByID[step.ProfileID],
				"selected":     step.Selected,
				"skip_reasons": step.SkipReasons,
			})
		}
		out := map[string]any{
			"role":             result.Role,
			"author_family":    result.AuthorFamily,
			"command_template": result.CommandTemplate,
			"ladder":           ladder,
			"owner_required":   result.OwnerRequired,
			"source":           "instance",
			"stale":            false,
		}
		if result.Preference != nil {
			out["preference"] = result.Preference
		}
		if result.Profile != nil {
			out["profile"] = map[string]any{
				"id":      result.Profile.Slug,
				"slug":    result.Profile.Slug,
				"version": result.Profile.Version,
				"family":  result.Profile.Family,
				"harness": result.Profile.Harness,
				"model":   result.Profile.Model,
				"effort":  result.Profile.Effort,
			}
		}
		if err := rt.printJSON(out); err != nil {
			return err
		}
	} else if result.Profile != nil {
		fmt.Fprintf(rt.stdout, "%s@%s — %s\n%s\n", result.Profile.Slug, result.Profile.Version, result.Profile.Family, result.CommandTemplate)
		for _, step := range result.Ladder {
			label := slugByID[step.ProfileID]
			if label == "" {
				label = step.ProfileID
			}
			status := "eligible fallback"
			if step.Selected {
				status = "selected"
			}
			if len(step.SkipReasons) > 0 {
				status = strings.Join(step.SkipReasons, "; ")
			}
			fmt.Fprintf(rt.stdout, "%s: %s\n", label, status)
		}
	}
	if !rt.jsonOut && result.Preference != nil {
		trace := result.Preference
		fmt.Fprintf(rt.stdout, "Why: %s · %s · set by %s · providers %s", trace.Kind, trace.Bucket, trace.SetBy, trace.Residency.Value)
		if trace.LockedBy != "" {
			fmt.Fprintf(rt.stdout, " · locked by %s", trace.LockedBy)
		}
		if trace.Residency.LoosenedLock {
			fmt.Fprint(rt.stdout, " · warning: loosens a provider lock")
		}
		if trace.Fallback != "" {
			fmt.Fprintf(rt.stdout, " · fallback: %s", trace.Fallback)
		}
		if trace.Blocked != "" {
			fmt.Fprintf(rt.stdout, " · blocked: %s", trace.Blocked)
		}
		fmt.Fprintln(rt.stdout)
	}
	if result.OwnerRequired {
		return rt.fail(fmt.Errorf("owner approval required; review gate remains closed"), "")
	}
	return nil
}

func (rt *runtime) cmdSession() *Command {
	return &Command{
		Name:  "session",
		Short: "Agent attribution sessions",
		Use:   "session <start>",
		subs:  []*Command{rt.cmdSessionStart()},
	}
}

func (rt *runtime) cmdSessionStart() *Command {
	var project, agent, format, bundle string
	var ruleOpts rulesOptions
	var receiveOpts rulesReceiveOptions
	return &Command{
		Name:  "start",
		Short: "Mint an attribution id, preview rules, or explicitly receive rules for a registered session",
		Use:   "session start --project KEY --agent NAME",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&agent, "agent", 0, "agent name (required)")
			fs.string(&format, "format", 0, "env (default) or json")
			fs.string(&bundle, "bundle", 0, "minimal (default) or full")
			ruleOpts.flags(fs)
			receiveOpts.flags(fs)
		},
		run: func(args []string) error {
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			if strings.TrimSpace(agent) == "" {
				return usagef("--agent is required")
			}
			if receiveOpts.Receive {
				if ruleOpts.Preview || bundle != "" || (format != "" && format != "json") {
					return usagef("--rules-receive is separate from preview and bundles; its output is JSON")
				}
				return rt.sessionRulesReceive(project, agent, ruleOpts, receiveOpts)
			}
			if receiveOpts.used() {
				return usagef("receiving flags require explicit --rules-receive")
			}
			if ruleOpts.Preview {
				return rt.sessionRules(project, ruleOpts)
			}
			bundle = strings.TrimSpace(strings.ToLower(bundle))
			format = strings.TrimSpace(strings.ToLower(format))
			full := bundle == "full" || format == "files"
			if bundle != "" && bundle != "minimal" && bundle != "full" {
				return usagef("--bundle must be minimal or full")
			}
			// The full bundle (harnessSessionFull) validates its own format.
			if !full && format == "" {
				if rt.jsonOut {
					format = "json"
				} else {
					format = "env"
				}
			}
			if !full && format != "env" && format != "json" {
				return usagef("invalid --format %q (expected env or json)", format)
			}
			name := strings.TrimSpace(agent)
			if !agentNameRE.MatchString(name) {
				return usagef("agent name must match %s", agentNameRE.String())
			}
			if _, err := rt.projectNode(project); err != nil {
				return err
			}
			sid, err := newUUIDv4()
			if err != nil {
				return err
			}
			if full {
				return rt.harnessSessionFull(project, name, format, sid)
			}
			if format == "json" {
				return rt.printJSON(map[string]any{"agent_name": name, "session_id": sid})
			}
			fmt.Fprintf(rt.stdout, "export PAIMOS_AGENT_NAME=%s\n", name)
			fmt.Fprintf(rt.stdout, "export PAIMOS_SESSION_ID=%s\n", sid)
			return nil
		},
	}
}

func (rt *runtime) cmdTell() *Command {
	var project, message, level string
	var expectsReply, actionRequest bool
	return &Command{
		Name:    "tell",
		Short:   "Send one durable inbox message",
		Use:     "tell <principal-uuid> --project KEY -m TEXT",
		minArgs: 1,
		maxArgs: 1,
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&message, "message", 'm', "message text (required)")
			fs.string(&level, "level", 0, "simple or steer; recorded only as classic compatibility")
			fs.bool(&expectsReply, "expects-reply", 0, "not available")
			fs.bool(&actionRequest, "action-request", 0, "not available")
		},
		run: func(args []string) error {
			address := strings.TrimSpace(args[0])
			if strings.Contains(address, ":") {
				return notYet(reasonHarnessAddress)
			}
			if expectsReply || actionRequest {
				return notYet(reasonExpectsReply)
			}
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			if strings.TrimSpace(message) == "" {
				return usagef("-m is required")
			}
			level = strings.TrimSpace(level)
			if level != "" && level != "simple" && level != "steer" {
				return usagef("--level must be simple or steer")
			}
			if !validUUID(address) {
				return usagef("tell address must be a principal UUID")
			}
			if _, err := rt.projectNode(project); err != nil {
				return err
			}
			me, err := rt.caller()
			if err != nil {
				return err
			}
			key, err := newUUIDv4()
			if err != nil {
				return err
			}
			var sent inboxMessage
			req := map[string]any{
				"recipient_principal_id": address,
				"body":                   message,
				"idempotency_key":        key,
			}
			if err := rt.do(http.MethodPost, "/api/inbox/messages", req, &sent); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(sent)
			}
			fmt.Fprintf(rt.stdout, "✓ %s → %s (sent)\nmessage: %s\n", me.Principal.Name, sent.RecipientPrincipalID, sent.ID)
			return nil
		},
	}
}

type inboxMessage struct {
	ID                   string `json:"id"`
	SenderPrincipalID    string `json:"sender_principal_id"`
	RecipientPrincipalID string `json:"recipient_principal_id"`
	Body                 string `json:"body"`
	SentEventID          int64  `json:"sent_event_id"`
}

func (rt *runtime) caller() (struct {
	Principal struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"principal"`
}, error) {
	var me struct {
		Principal struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"principal"`
	}
	if err := rt.do(http.MethodGet, "/api/me", nil, &me); err != nil {
		return me, err
	}
	return me, nil
}

func (rt *runtime) cmdListen() *Command {
	var as, project, deliver string
	var follow, ack bool
	return &Command{
		Name:  "listen",
		Short: "Read the authenticated principal's inbox",
		Use:   "listen --project KEY [--as NAME] [--ack]",
		addFlags: func(fs *flagSet) {
			fs.string(&as, "as", 0, "principal name or UUID; must be the caller")
			fs.string(&project, "project", 0, "project key (required)")
			fs.string(&deliver, "deliver", 0, "not available")
			fs.bool(&follow, "follow", 0, "not available")
			fs.bool(&ack, "ack", 0, "acknowledge each printed message")
		},
		run: func(args []string) error {
			if strings.Contains(as, ":") {
				return notYet(reasonListenAs)
			}
			if follow || strings.TrimSpace(deliver) != "" {
				return notYet(reasonListenFollow)
			}
			if strings.TrimSpace(project) == "" {
				return usagef("--project is required")
			}
			if _, err := rt.projectNode(project); err != nil {
				return err
			}
			me, err := rt.caller()
			if err != nil {
				return err
			}
			as = strings.TrimSpace(as)
			if as != "" && as != me.Principal.Name && !strings.EqualFold(as, me.Principal.ID) {
				return rt.fail(fmt.Errorf("listen can only read the authenticated principal"), "")
			}
			var page struct {
				Items []inboxMessage `json:"items"`
			}
			if err := rt.do(http.MethodGet, "/api/inbox/messages?wait_ms=0", nil, &page); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(page)
			}
			if len(page.Items) == 0 {
				fmt.Fprintln(rt.stdout, "(no messages)")
				return nil
			}
			for _, item := range page.Items {
				fmt.Fprintf(rt.stdout, "cursor=%d  %s  %s → %s\n%s\n", item.SentEventID, item.ID, item.SenderPrincipalID, item.RecipientPrincipalID, item.Body)
				if ack {
					if err := rt.do(http.MethodPost, "/api/inbox/messages/"+url.PathEscape(item.ID)+"/ack", nil, nil); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

func (rt *runtime) cmdMessage() *Command {
	return &Command{
		Name:  "message",
		Short: "Inbox targets",
		Use:   "message <target|deliveries>",
		subs: []*Command{
			rt.cmdMessageTarget(),
			{
				Name:  "deliveries",
				Short: "Not available",
				Use:   "message deliveries",
				run: func(args []string) error {
					return notYet(reasonDeliveries)
				},
			},
		},
	}
}

func (rt *runtime) cmdMessageTarget() *Command {
	return &Command{
		Name:  "target",
		Short: "Delivery targets for the caller",
		Use:   "message target <set|list>",
		subs: []*Command{
			rt.cmdMessageTargetSet(),
			rt.cmdMessageTargetList(),
		},
	}
}

func (rt *runtime) cmdMessageTargetSet() *Command {
	var project, address, adapter, kind, principal, webhook, keyFile string
	return &Command{
		Name:  "set",
		Short: "Register a pull or webhook target",
		Use:   "message target set --principal UUID --kind pull",
		addFlags: func(fs *flagSet) {
			fs.string(&project, "project", 0, "accepted when a project check is required")
			fs.string(&address, "address", 0, "classic harness:agent address; not available")
			fs.string(&adapter, "adapter", 0, "classic adapter; not available")
			fs.string(&kind, "kind", 0, "pull or webhook")
			fs.string(&principal, "principal", 0, "recipient principal UUID (default: caller)")
			fs.string(&webhook, "webhook-url", 0, "https URL for kind webhook")
			fs.string(&keyFile, "target-key-file", 0, "classic routine key; not available")
		},
		run: func(args []string) error {
			if strings.TrimSpace(address) != "" || strings.TrimSpace(adapter) != "" || strings.TrimSpace(keyFile) != "" || classicTargetKind(kind) {
				return notYet(reasonClassicTarget)
			}
			kind = strings.TrimSpace(kind)
			if kind != "pull" && kind != "webhook" {
				return usagef("--kind must be pull or webhook")
			}
			if kind == "webhook" && strings.TrimSpace(webhook) == "" {
				return usagef("--webhook-url is required for kind webhook")
			}
			if kind == "pull" && strings.TrimSpace(webhook) != "" {
				return usagef("pull targets have no webhook URL")
			}
			me, err := rt.caller()
			if err != nil {
				return err
			}
			principal = strings.TrimSpace(principal)
			if principal == "" {
				principal = me.Principal.ID
			}
			if !validUUID(principal) {
				return usagef("--principal must be a UUID")
			}
			req := map[string]any{"principal_id": principal, "kind": kind}
			if kind == "webhook" {
				req["webhook_url"] = strings.TrimSpace(webhook)
			}
			var created inboxTarget
			if err := rt.do(http.MethodPost, "/api/inbox/targets", req, &created); err != nil {
				return err
			}
			if rt.jsonOut {
				return rt.printJSON(targetView(created))
			}
			fmt.Fprintf(rt.stdout, "✓ enabled target %s for %s (%s)\n", created.ID, created.PrincipalID, created.Kind)
			return nil
		},
	}
}

func classicTargetKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "codex_thread", "agentd_session", "https_webhook", "claude_session":
		return true
	default:
		return false
	}
}

type inboxTarget struct {
	ID          string  `json:"id"`
	PrincipalID string  `json:"principal_id"`
	Kind        string  `json:"kind"`
	WebhookURL  *string `json:"webhook_url,omitempty"`
	Enabled     bool    `json:"enabled"`
}

type targetPrint struct {
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
}

func targetView(t inboxTarget) targetPrint {
	return targetPrint{ID: t.ID, PrincipalID: t.PrincipalID, Kind: t.Kind, Enabled: t.Enabled}
}

func (rt *runtime) cmdMessageTargetList() *Command {
	return &Command{
		Name:  "list",
		Short: "List the caller's delivery targets",
		Use:   "message target list",
		run: func(args []string) error {
			var items []inboxTarget
			if err := rt.do(http.MethodGet, "/api/inbox/targets", nil, &items); err != nil {
				return err
			}
			views := make([]targetPrint, 0, len(items))
			for _, item := range items {
				views = append(views, targetView(item))
			}
			if rt.jsonOut {
				return rt.printJSON(views)
			}
			if len(views) == 0 {
				fmt.Fprintln(rt.stdout, "(no targets)")
				return nil
			}
			for _, item := range views {
				state := "disabled"
				if item.Enabled {
					state = "enabled"
				}
				fmt.Fprintf(rt.stdout, "%s  %s  %s  %s\n", item.ID, item.Kind, item.PrincipalID, state)
			}
			return nil
		},
	}
}

func stubCommand(name, use, reason string, subs ...string) *Command {
	if len(subs) == 0 {
		return &Command{
			Name:  name,
			Short: "Not available on Aeon",
			Use:   use,
			run: func(args []string) error {
				return notYet(reason)
			},
		}
	}
	children := make([]*Command, 0, len(subs))
	for _, sub := range subs {
		subName := sub
		children = append(children, &Command{
			Name:    subName,
			Short:   "Not available on Aeon",
			Use:     name + " " + subName,
			maxArgs: -1,
			run: func(args []string) error {
				return notYet(reason)
			},
		})
	}
	return &Command{
		Name:  name,
		Short: "Not available on Aeon",
		Use:   use,
		subs:  children,
	}
}

func (rt *runtime) compatStubs() []*Command {
	return []*Command{
		rt.cmdAnchors(),
		rt.cmdSkill(),
		rt.cmdRunAgent(),
		rt.cmdBaselineBatch(),
		rt.cmdSync(),
		rt.cmdHarnessV2(),
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
)

type rulesOptions struct {
	Preview                                                                 bool
	Tenant, Person, Agent, Role, Harness, Task, Cache, Floor, FloorSHA, Out string
}

func (o *rulesOptions) flags(fs *flagSet) {
	fs.bool(&o.Preview, "rules-preview", 0, "opt in to merged rules; never replace active instructions")
	fs.string(&o.Tenant, "rules-tenant", 0, "exact tenant UUID required for online and offline rules")
	fs.string(&o.Person, "rules-person", 0, "authorized person UUID")
	fs.string(&o.Agent, "rules-agent", 0, "named agent principal UUID (required for agent callers)")
	fs.string(&o.Role, "rules-role", 0, "coordinator, builder, reviewer, or operator")
	fs.string(&o.Harness, "rules-harness", 0, "claude-code, codex, grok, pi, or cursor")
	fs.string(&o.Task, "rules-task", 0, "optional exact task UUID")
	fs.string(&o.Cache, "rules-cache", 0, "explicit private .json cache in an existing physical directory")
	fs.string(&o.Floor, "rules-floor", 0, "independently retained private locked-floor .txt file")
	fs.string(&o.FloorSHA, "rules-floor-sha256", 0, "trusted exact SHA256 of the retained company floor")
	fs.string(&o.Out, "rules-out", 0, "optional new .txt preview output (never overwrites)")
}

// persistStaleCache marks a verified cache that was served because Aeon was
// unreachable. A cache that was not used (corrupt, expired, wrong context)
// is left untouched. An already-marked cache is not rewritten.
func persistStaleCache(path, instance string, c rules.Context, raw []byte, now time.Time) error {
	marked, err := rules.MarkStale(raw, instance, c, now)
	if err != nil {
		return err
	}
	if bytes.Equal(marked, raw) {
		return nil
	}
	return rules.WriteFile(path, marked, true)
}

func networkUnavailable(err error) bool {
	var se *client.StatusError
	if errors.As(err, &se) {
		return se.Status == 502 || se.Status == 503 || se.Status == 504
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// sessionRules does not call the old knowledge-bundle renderer or write its
// active files. Offline operation requires exact UUID selectors, so it never
// resolves a project key or person name from a different tenant's old cache.
func (rt *runtime) sessionRules(project string, o rulesOptions) error {
	c := rules.Context{TenantID: o.Tenant, ProjectID: project, PersonID: o.Person, AgentID: o.Agent, Role: o.Role, Harness: o.Harness, TaskID: o.Task}
	if err := rules.ValidateContext(c); err != nil {
		return usagef("rules preview requires --project UUID and all exact rules selectors: %s", err)
	}
	if o.Cache == "" || o.Floor == "" || o.FloorSHA == "" {
		return usagef("rules preview requires --rules-cache, --rules-floor and --rules-floor-sha256")
	}
	raw, err := rules.ReadFile(o.Floor, rules.MaxBytes)
	if err != nil {
		return err
	}
	floor, err := rules.VerifyFloor(raw, o.FloorSHA)
	if err != nil {
		return err
	}
	api, err := rt.api()
	if err != nil {
		return err
	}
	q := url.Values{"project_id": {c.ProjectID}, "person_id": {c.PersonID}, "role": {c.Role}, "harness": {c.Harness}}
	if c.AgentID != "" {
		q.Set("agent_id", c.AgentID)
	}
	if c.TaskID != "" {
		q.Set("task_id", c.TaskID)
	}
	var m rules.Merged
	requestCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = api.Do(requestCtx, http.MethodGet, "/api/rules/merged?"+q.Encode(), nil, &m)
	stale := false
	gap := ""
	now := time.Now().UTC()
	if err != nil {
		if !networkUnavailable(err) {
			return rt.fail(err, api.Token)
		}
		stale = true
		cache, _ := rules.ReadFile(o.Cache, rules.MaxCacheBytes)
		m, err = rules.Offline(cache, api.BaseURL, c, floor, now)
		if err != nil {
			gap = err.Error()
		} else if err = persistStaleCache(o.Cache, api.BaseURL, c, cache, now); err != nil {
			return err
		}
		if m.Body == "" {
			return err
		}
	} else {
		if err = rules.ValidateMerged(m, c, now); err != nil {
			return err
		}
		// The independent pinned floor must still be present. Updating it is an
		// explicit operator step; fetching a bundle never silently changes the pin.
		for _, line := range strings.Split(strings.TrimSpace(floor), "\n") {
			if !strings.Contains("\n"+m.Body, "\n"+line+"\n") {
				return fmt.Errorf("server rules omit the pinned locked floor; review the floor change explicitly")
			}
		}
		cache, err := rules.EncodeCache(api.BaseURL, m, now)
		if err != nil {
			return err
		}
		if err = rules.WriteFile(o.Cache, cache, true); err != nil {
			return err
		}
	}
	rendered, err := renderRulesThroughHarness(m)
	if err != nil {
		return err
	}
	if o.Out != "" {
		if err = rules.WriteFile(o.Out, []byte(rendered.Body), false); err != nil {
			return err
		}
	}
	// A preview is only proposed received content. It is not an installed
	// instruction file or a revision of the session's actual provenance.
	return rt.printJSON(map[string]any{
		"rules": m, "stale": stale, "gap": gap,
		"suggested_name": rendered.SuggestedPath, "output_path": o.Out,
		"proposed_received_payload": map[string]any{"body_sha256": m.SHA256, "version": m.Version, "byte_size": m.ByteSize},
		"provenance_recorded":       false, "execution_verified": false,
	})
}

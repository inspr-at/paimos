// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/runtimehealth"
	"gopkg.in/yaml.v3"
)

func runtimeLayer(name string, state runtimehealth.State, code, action string) runtimehealth.Layer {
	return runtimehealth.Layer{Name: name, State: state, Code: code, Action: action}
}

// A doctor read must not invoke loadConfig's legacy credential migration.
// Named credentials follow the ordinary instance keyring authority; ambient
// environment credentials never replace them and no credential is rendered.
func runtimeNamedClient(name string) (*Client, error) {
	if strings.TrimSpace(os.Getenv(envURL)) != "" || strings.TrimSpace(os.Getenv(envPPMURL)) != "" {
		return nil, errors.New("named runtime conflicts with ambient instance")
	}
	path, e := configPath()
	if e != nil {
		return nil, errors.New("named config unavailable")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("named config unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return nil, errors.New("named config unavailable")
	}
	var cfg Config
	if yaml.Unmarshal(b, &cfg) != nil {
		return nil, errors.New("named config invalid")
	}
	inst, ok := cfg.Instances[name]
	if !ok || inst.APIKey != "" {
		return nil, errors.New("named config missing or credential migration required")
	}
	key, _, e := resolveAPIKey(name)
	if e != nil || key == "" {
		return nil, errors.New("named credential unavailable")
	}
	inst.APIKey = key
	u, e := url.Parse(inst.URL)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return nil, errors.New("named instance URL invalid")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("named instance requires HTTPS")
	}
	client := newClient(inst)
	client.http.Timeout = 4 * time.Second
	return client, nil
}

// Runtime diagnostics use the established authenticated client headers and
// redirect policy, but cap response sizes and never return a server body.
func runtimeRead(ctx context.Context, c *Client, path string, out any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if e != nil {
		return errors.New("runtime remote request unavailable")
	}
	c.prepareRequest(req, false, "application/json", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return errors.New("runtime remote request unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("runtime remote authorization or endpoint unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(b) > 1<<20 || json.Unmarshal(b, out) != nil {
		return errors.New("runtime remote evidence invalid")
	}
	return nil
}
func runtimeRemoteProbe(name, expected string, projectID int64) runtimehealth.RemoteProbe {
	return func(ctx context.Context) runtimehealth.RemoteEvidence {
		e := runtimehealth.RemoteEvidence{
			ProjectID: projectID,
			Auth:      runtimeLayer("cli_auth", runtimehealth.Unknown, "named_auth_unavailable", "run paimos auth login for the named instance"),
			Identity:  runtimeLayer("server_identity", runtimehealth.Unknown, "server_identity_unverified", "verify the named deployment"),
			Agents:    runtimeLayer("canonical_agents", runtimehealth.Unknown, "project_not_selected", "supply --project for an authorized project"),
			Profiles:  runtimeLayer("dispatch_profiles", runtimehealth.Unknown, "profiles_unverified", "inspect immutable execution profiles"),
			Targets:   runtimeLayer("targets", runtimehealth.Unknown, "target_ownership_unverified", "supply --project; target inspection requires existing administration authority"),
		}
		c, err := runtimeNamedClient(name)
		if err != nil {
			return e
		}
		var me struct {
			User map[string]json.RawMessage `json:"user"`
		}
		if runtimeRead(ctx, c, "/api/auth/me", &me) != nil || len(me.User) == 0 {
			return e
		}
		e.Auth = runtimeLayer("cli_auth", runtimehealth.Known, "named_auth_verified", "")
		var health orchestratorHealth
		if runtimeRead(ctx, c, "/api/health", &health) != nil {
			return e
		}
		if !health.AgentBusIdentityEnforced || health.DeploymentInstance != expected || health.AgentBusInstance != expected {
			e.Identity = runtimeLayer("server_identity", runtimehealth.ActionRequired, "deployment_identity_mismatch", "correct the named instance or expected deployment before setup")
			return e
		}
		e.Identity = runtimeLayer("server_identity", runtimehealth.Known, "authenticated_deployment_verified", "")
		var profiles struct {
			Profiles []dispatchprofile.Profile `json:"dispatch_profiles"`
		}
		if runtimeRead(ctx, c, "/api/ai/execution-options?dispatch_only=1", &profiles) == nil && len(profiles.Profiles) > 0 && len(profiles.Profiles) <= 64 {
			valid := true
			seen := map[string]bool{}
			for _, p := range profiles.Profiles {
				k := p.ID + "\x00" + p.Version
				if dispatchprofile.Validate(p) != nil || seen[k] {
					valid = false
				}
				seen[k] = true
			}
			if valid {
				e.Profiles = runtimeLayer("dispatch_profiles", runtimehealth.Known, "immutable_profiles_available", "")
			}
		}
		if projectID > 0 {
			// Decode only identity metadata; body/instructions never enter reports.
			var rows []struct {
				ID        int64  `json:"id"`
				ProjectID int64  `json:"project_id"`
				Name      string `json:"name"`
			}
			if runtimeRead(ctx, c, fmt.Sprintf("/api/projects/%d/agents", projectID), &rows) == nil {
				valid := len(rows) > 0
				for _, a := range rows {
					if a.ID <= 0 || a.ProjectID != projectID || !orchestratorAgentKeyPattern.MatchString(a.Name) {
						valid = false
					}
				}
				if valid {
					e.Agents = runtimeLayer("canonical_agents", runtimehealth.Known, "canonical_agents_available", "")
				} else {
					e.Agents = runtimeLayer("canonical_agents", runtimehealth.ActionRequired, "canonical_agents_missing", "configure project agents explicitly")
				}
			}
			var targets struct {
				Targets []json.RawMessage `json:"targets"`
			}
			if runtimeRead(ctx, c, fmt.Sprintf("/api/projects/%d/message-targets", projectID), &targets) == nil {
				// A project target list is not proof of this daemon's receiver ownership.
				// Fresh owned-consumer evidence must attest the target revision and lease.
				if len(targets.Targets) == 0 {
					e.Targets = runtimeLayer("targets", runtimehealth.ActionRequired, "no_registered_targets", "register exact targets through the authorized target workflow")
				} else {
					e.Targets = runtimeLayer("targets", runtimehealth.Unknown, "target_registration_observed_ownership_unverified", "verify the exact owned daemon consumer and target revision")
				}
			}
		}
		return e
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
)

type rulesReceiveOptions struct {
	Receive, Retry                                 bool
	Session, LeaseFile, RequestID, Revision, State string
}

func (o *rulesReceiveOptions) flags(fs *flagSet) {
	fs.bool(&o.Receive, "rules-receive", 0, "explicitly receive rules bytes and report receipt for an existing public generation")
	fs.bool(&o.Retry, "rules-retry", 0, "explicitly retry the identical saved online receipt; never overwrite output")
	fs.string(&o.Session, "session", 0, "already-registered public session UUID (receiving only)")
	fs.string(&o.LeaseFile, "worker-lease-file", 0, "private lease file for that generation (receiving only)")
	fs.string(&o.RequestID, "rules-request-id", 0, "explicit lowercase receipt request UUID; retain for retry")
	fs.string(&o.Revision, "rules-expected-revision", 0, "explicit current receipt revision, initially 0 (not session/provenance revision)")
	fs.string(&o.State, "rules-state", 0, "new private .json checkpoint; reuse only with --rules-retry")
}

func (o rulesReceiveOptions) used() bool {
	return o.Retry || o.Session != "" || o.LeaseFile != "" || o.RequestID != "" || o.Revision != "" || o.State != ""
}

// This immutable checkpoint is not a queue. Only an explicit --rules-retry can
// submit it, and offline artifacts can never be submitted by this command.
// Like the existing cache its digest detects corruption, not hostile local edits.
type rulesReceiveState struct {
	Schema   string                    `json:"schema"`
	Instance string                    `json:"instance"`
	Session  string                    `json:"session_id"`
	Agent    string                    `json:"agent_name"`
	Output   string                    `json:"output_path"`
	FloorSHA string                    `json:"floor_sha256"`
	Bundle   rules.Merged              `json:"bundle"`
	Request  harness.RulesReceiptWrite `json:"request"`
	Gap      string                    `json:"gap"`
	SHA256   string                    `json:"sha256"`
}

func (s rulesReceiveState) digest() string {
	s.SHA256 = ""
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Keep raw server errors and credentials out of both stdout and stderr. HTTP
// status is sufficient for recovery; malformed responses are never evidence.
func rulesReceiveError(err error) error {
	var se *client.StatusError
	if errors.As(err, &se) {
		return &client.StatusError{Status: se.Status}
	}
	return errors.New("rules request unavailable or invalid; no receipt confirmed")
}

func rulesReceiveDo(api *client.Client, method, path, lease string, body, dest any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var headers map[string]string
	if lease != "" {
		headers = map[string]string{"X-Aeon-Worker-Lease": lease}
	}
	return api.DoWithHeaders(ctx, method, path, body, dest, headers)
}

func rulesReceiveFloor(m rules.Merged, floor string) error {
	// Receiving requires the exact reviewed floor, including additions. Preview
	// retains its existing containment check and never records a receipt.
	if m.Floor != floor {
		return errors.New("published locked floor changed; review and pin it explicitly")
	}
	return nil
}

func rulesReceiveFetch(api *client.Client, c rules.Context, o rulesOptions, floor string) (rules.Merged, string, string, error) {
	q := url.Values{"project_id": {c.ProjectID}, "person_id": {c.PersonID}, "agent_id": {c.AgentID}, "role": {c.Role}, "harness": {c.Harness}}
	if c.TaskID != "" {
		q.Set("task_id", c.TaskID)
	}
	var m rules.Merged
	err := rulesReceiveDo(api, http.MethodGet, "/api/rules/merged?"+q.Encode(), "", nil, &m)
	now := time.Now().UTC()
	if err != nil {
		if !networkUnavailable(err) {
			return m, "", "", rulesReceiveError(err)
		}
		cache, _ := rules.ReadFile(o.Cache, rules.MaxCacheBytes)
		m, err = rules.Offline(cache, api.BaseURL, c, floor, now)
		if m.Body == "" {
			return m, "", "", err
		}
		if err != nil {
			return m, "floor-only", err.Error(), nil
		}
		if err = persistStaleCache(o.Cache, api.BaseURL, c, cache, now); err != nil {
			return m, "", "", err
		}
		return m, "cache", "network unavailable; publication and generation not checked online", nil
	}
	if err = rules.ValidateMerged(m, c, now); err != nil {
		return m, "", "", err
	}
	if err = rulesReceiveFloor(m, floor); err != nil {
		return m, "", "", err
	}
	cache, err := rules.EncodeCache(api.BaseURL, m, now)
	if err != nil {
		return m, "", "", err
	}
	if err = rules.WriteFile(o.Cache, cache, true); err != nil {
		return m, "", "", err
	}
	return m, "online", "", nil
}

func rulesReceiveGeneration(api *client.Client, c rules.Context, session, agent string) error {
	var me client.Me
	if err := rulesReceiveDo(api, http.MethodGet, "/api/me", "", nil, &me); err != nil {
		return rulesReceiveError(err)
	}
	if me.Principal.ID != c.AgentID || me.Principal.Name != agent || me.Principal.Kind != "agent" || me.Tenant.ID != c.TenantID {
		return errors.New("rules receiving caller context rejected")
	}
	var s harness.Session
	if err := rulesReceiveDo(api, http.MethodGet, harnessPath(c.ProjectID, session), "", nil, &s); err != nil {
		return rulesReceiveError(err)
	}
	h := s.Harness
	if h == "claude" {
		h = "claude-code"
	}
	if s.ID != session || s.ProjectID != c.ProjectID || s.AgentPrincipalID != c.AgentID || h != c.Harness || s.StoppedAt != nil || s.ArchivedAt != nil || (c.TaskID != "" && (s.TicketNodeID == nil || *s.TicketNodeID != c.TaskID)) {
		return errors.New("rules receiving registered generation context rejected")
	}
	return nil
}

func (rt *runtime) sessionRulesReceive(project, agent string, o rulesOptions, r rulesReceiveOptions) error {
	c := rules.Context{TenantID: o.Tenant, ProjectID: project, PersonID: o.Person, AgentID: o.Agent, Role: o.Role, Harness: o.Harness, TaskID: o.Task}
	revision, err := strconv.ParseInt(r.Revision, 10, 64)
	if err != nil || revision < 0 || revision == math.MaxInt64 || !validUUID(r.RequestID) || strings.ToLower(r.RequestID) != r.RequestID || !validUUID(r.Session) || !agentNameRE.MatchString(agent) || c.AgentID == "" || rules.ValidateContext(c) != nil {
		return usagef("receiving requires exact rules UUID selectors, registered --session, --agent, --rules-request-id and explicit --rules-expected-revision")
	}
	if o.Cache == "" || o.Floor == "" || o.FloorSHA == "" || o.Out == "" || filepath.Ext(o.Out) != ".txt" || r.State == "" || filepath.Ext(r.State) != ".json" || r.LeaseFile == "" || r.LeaseFile == "-" {
		return usagef("receiving requires --rules-cache, --rules-floor, --rules-floor-sha256, new --rules-out .txt, --rules-state .json and --worker-lease-file (not stdin)")
	}
	// Do not allow a cache refresh to replace the immutable checkpoint.
	paths := map[string]bool{}
	for _, path := range []string{o.Out, o.Cache, o.Floor, r.State, r.LeaseFile} {
		abs, e := filepath.Abs(path)
		if e != nil || paths[abs] {
			return usagef("receiving files must have distinct paths")
		}
		paths[abs] = true
	}
	output, _ := filepath.Abs(o.Out)
	raw, err := rules.ReadFile(o.Floor, rules.CeilingBytes)
	if err != nil {
		return err
	}
	floor, err := rules.VerifyFloor(raw, o.FloorSHA)
	if err != nil {
		return err
	}
	lease, err := rt.harnessSecret(r.LeaseFile, "worker-lease-file")
	if err != nil {
		return errors.New("worker lease file rejected")
	}
	if len(lease) < 32 {
		return usagef("worker lease must contain at least 32 characters")
	}
	api, err := rt.api()
	if err != nil {
		return err
	}
	api.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	state := rulesReceiveState{Schema: "aeon.rules.receive.v1", Instance: api.BaseURL, Session: r.Session, Agent: agent, Output: output, FloorSHA: o.FloorSHA}
	if r.Retry {
		raw, err = rules.ReadFile(r.State, rules.MaxCacheBytes)
		if err != nil {
			return err
		}
		if json.Unmarshal(raw, &state) != nil || state.SHA256 != state.digest() || state.Schema != "aeon.rules.receive.v1" || state.Instance != api.BaseURL || state.Session != r.Session || state.Agent != agent || state.Output != output || state.FloorSHA != o.FloorSHA || state.Request.Context != c || state.Request.RequestID != r.RequestID || state.Request.ExpectedRevision == nil || *state.Request.ExpectedRevision != revision {
			return errors.New("rules retry checkpoint or exact selectors do not match")
		}
		if state.Request.Source != "online" {
			return errors.New("offline artifacts cannot be replayed as receipts; receive afresh into new files when online")
		}
		if err = rules.ValidateMerged(state.Bundle, c, time.Now().UTC()); err != nil {
			return err
		}
		if err = rulesReceiveFloor(state.Bundle, floor); err != nil {
			return err
		}
		if state.Request.BodySHA256 != state.Bundle.SHA256 || state.Request.Version != state.Bundle.Version || state.Request.ByteSize == nil || *state.Request.ByteSize != state.Bundle.ByteSize {
			return errors.New("rules retry receipt does not match saved bytes")
		}
		// Recheck current read authorization/floor without replacing original
		// metadata, output or cache. A newer publication cannot alter this retry.
		// Retry must succeed online, including after a lost POST response.
		q := url.Values{"project_id": {c.ProjectID}, "person_id": {c.PersonID}, "agent_id": {c.AgentID}, "role": {c.Role}, "harness": {c.Harness}}
		if c.TaskID != "" {
			q.Set("task_id", c.TaskID)
		}
		var current rules.Merged
		if err = rulesReceiveDo(api, http.MethodGet, "/api/rules/merged?"+q.Encode(), "", nil, &current); err != nil {
			return rulesReceiveError(err)
		}
		if err = rules.ValidateMerged(current, c, time.Now().UTC()); err != nil {
			return err
		}
		if err = rulesReceiveFloor(current, floor); err != nil {
			return err
		}
	} else {
		var source string
		state.Bundle, source, state.Gap, err = rulesReceiveFetch(api, c, o, floor)
		if err != nil {
			return err
		}
		size := state.Bundle.ByteSize
		state.Request = harness.RulesReceiptWrite{RequestID: r.RequestID, ExpectedRevision: &revision, Context: c, BodySHA256: state.Bundle.SHA256, Version: state.Bundle.Version, ByteSize: &size, Source: source}
	}
	if state.Request.Source == "online" {
		if err = rulesReceiveGeneration(api, c, r.Session, agent); err != nil {
			return err
		}
	}
	// Generation reads can take time. Do not write rules that expired while
	// checking identity, or submit a receipt after a slow filesystem operation.
	if state.Request.Source != "floor-only" {
		if err = rules.ValidateMerged(state.Bundle, c, time.Now().UTC()); err != nil {
			return err
		}
	}
	rendered, err := renderRulesThroughHarness(state.Bundle)
	if err != nil {
		return err
	}
	if !r.Retry {
		state.SHA256 = state.digest()
		raw, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if err = rules.WriteFile(r.State, raw, false); err != nil {
			return fmt.Errorf("cannot create rules checkpoint (use --rules-retry only for an existing exact attempt): %w", err)
		}
	}
	if r.Retry {
		raw, err = rules.ReadFile(o.Out, rules.CeilingBytes)
		if err == nil && !bytes.Equal(raw, []byte(rendered.Body)) {
			return errors.New("existing output differs from checkpoint; refusing overwrite or receipt")
		}
	}
	if !r.Retry || err != nil {
		if err = rules.WriteFile(o.Out, []byte(rendered.Body), false); err != nil {
			return fmt.Errorf("rules checkpoint retained; output not confirmed, no receipt submitted: %w", err)
		}
	}
	result := map[string]any{
		"session_id": r.Session, "request_id": r.RequestID, "expected_revision": revision,
		"source": state.Request.Source, "stale": state.Request.Source != "online", "gap": state.Gap,
		"output_path": output, "state_path": r.State,
		"body_sha256": state.Bundle.SHA256, "version": state.Bundle.Version, "byte_size": state.Bundle.ByteSize,
		"bytes_received": true, "receipt_recorded": false, "receipt_status": "not_submitted_offline",
		"provenance_recorded": false, "publication_verified": false, "load_verified": false,
		"execution_verified": false, "authority_granted": false, "complete": false,
	}
	if state.Request.Source != "online" {
		return rt.printJSON(result)
	}
	var recorded struct {
		Receipt  harness.RulesReceipt `json:"receipt"`
		Replayed bool                 `json:"replayed"`
	}
	err = rules.ValidateMerged(state.Bundle, c, time.Now().UTC())
	if err == nil {
		err = rulesReceiveDo(api, http.MethodPost, harnessPath(c.ProjectID, r.Session)+"/rules-receipts", lease, state.Request, &recorded)
	}
	if err == nil {
		a := recorded.Receipt
		if !validUUID(a.ID) || a.SessionID != r.Session || a.Revision != revision+1 || a.RecordedBy != c.AgentID || a.RecordedAt.IsZero() || !reflect.DeepEqual(a.Request, state.Request) || a.Evidence != "worker_reported_received" || a.PublicationVerified || a.LoadVerified || a.ExecutionVerified || a.AuthorityGranted {
			err = errors.New("invalid receipt response")
		}
	}
	if err != nil {
		result["receipt_status"] = "unconfirmed"
		var se *client.StatusError
		if errors.As(err, &se) && se.Status >= 400 && se.Status < 500 {
			result["receipt_status"] = "rejected"
		}
		if e := rt.printJSON(result); e != nil {
			return e
		}
		return fmt.Errorf("bytes retained; receipt not confirmed; explicitly retry with identical flags plus --rules-retry: %w", rulesReceiveError(err))
	}
	result["receipt_recorded"], result["receipt_status"], result["complete"], result["provenance_recorded"] = true, "recorded", true, true
	result["receipt_id"], result["receipt_revision"], result["replayed"] = recorded.Receipt.ID, recorded.Receipt.Revision, recorded.Replayed
	return rt.printJSON(result)
}

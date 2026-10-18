// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/plugins/fence"
)

const (
	maxID     = 128
	maxText   = 256
	maxItems  = 32
	maxSchema = 1 << 16
)

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Manifest is a compiled first-party declaration. DigestSHA256 is the sha256
// of the canonical declaration plus the step-permission bindings.
type Manifest struct {
	ID             string         `json:"id"`
	Version        string         `json:"version"`
	DigestSHA256   string         `json:"digest_sha256"`
	Owner          string         `json:"owner"`
	Permissions    []string       `json:"permissions"`
	NodeKinds      []NodeKind     `json:"node_kinds"`
	Views          []View         `json:"views"`
	WorkflowSteps  []WorkflowStep `json:"workflow_steps"`
	AgentTools     []Capability   `json:"agent_tools"`
	Integrations   []Capability   `json:"integrations"`
	BackgroundJobs []Capability   `json:"background_jobs"`
}

// NodeKind is a node kind and its JSON field schema.
type NodeKind struct {
	Slug              string          `json:"slug"`
	FieldSchema       json.RawMessage `json:"field_schema"`
	AllowedChildKinds []string        `json:"allowed_child_kinds"`
}

// View is a view and the panels it contributes.
type View struct {
	ID     string   `json:"id"`
	Panels []string `json:"panels"`
}

// WorkflowStep is one workflow step and the gates it declares.
type WorkflowStep struct {
	Key   string   `json:"key"`
	Gates []string `json:"gates"`
}

// Capability is a tool, integration, or background job.
type Capability struct {
	ID         string `json:"id"`
	Permission string `json:"permission"`
}

// Plugin is a compiled manifest plus the implementations its declaration requires.
type Plugin struct {
	Manifest        Manifest
	StepPermissions map[string]string
	Kinds           NodeKindContributor
	Views           ViewProvider
	Steps           StepPlugin
	Tools           ToolProvider
	Integrations    IntegrationProvider
	Jobs            JobProvider
}

type stepBinding struct {
	Key        string `json:"key"`
	Permission string `json:"permission"`
}

// Digest is the canonical sha256 of p. It ignores Manifest.DigestSHA256.
func Digest(p Plugin) (string, error) {
	manifest, bindings, err := normalize(p)
	if err != nil {
		return "", err
	}
	return hashPlugin(manifest, bindings)
}

func normalize(p Plugin) (Manifest, map[string]string, error) {
	m := p.Manifest
	if !validID(m.ID) {
		return Manifest{}, nil, fmt.Errorf("plugins: invalid id")
	}
	if !plainText(m.Version, maxID) || !plainText(m.Owner, maxText) {
		return Manifest{}, nil, fmt.Errorf("plugins: invalid version or owner")
	}
	perms, err := normalizeNames(m.Permissions, "permission", fence.KnownPermission)
	if err != nil {
		return Manifest{}, nil, err
	}
	slices.Sort(perms)
	ceiling := setOf(perms)
	kinds, err := normalizeKinds(m.NodeKinds)
	if err != nil {
		return Manifest{}, nil, err
	}
	views, err := normalizeViews(m.Views)
	if err != nil {
		return Manifest{}, nil, err
	}
	steps, err := normalizeSteps(m.WorkflowSteps)
	if err != nil {
		return Manifest{}, nil, err
	}
	tools, err := normalizeCaps(m.AgentTools, "agent tool", ceiling)
	if err != nil {
		return Manifest{}, nil, err
	}
	integrations, err := normalizeCaps(m.Integrations, "integration", ceiling)
	if err != nil {
		return Manifest{}, nil, err
	}
	jobs, err := normalizeCaps(m.BackgroundJobs, "background job", ceiling)
	if err != nil {
		return Manifest{}, nil, err
	}
	bindings, err := normalizeBindings(steps, p.StepPermissions, ceiling)
	if err != nil {
		return Manifest{}, nil, err
	}
	if err := matchImpl(len(kinds) > 0, p.Kinds != nil, "node kinds"); err != nil {
		return Manifest{}, nil, err
	}
	if err := matchImpl(len(views) > 0, p.Views != nil, "views"); err != nil {
		return Manifest{}, nil, err
	}
	if err := matchImpl(len(steps) > 0, p.Steps != nil, "workflow steps"); err != nil {
		return Manifest{}, nil, err
	}
	if err := matchImpl(len(tools) > 0, p.Tools != nil, "agent tools"); err != nil {
		return Manifest{}, nil, err
	}
	if err := matchImpl(len(integrations) > 0, p.Integrations != nil, "integrations"); err != nil {
		return Manifest{}, nil, err
	}
	if err := matchImpl(len(jobs) > 0, p.Jobs != nil, "background jobs"); err != nil {
		return Manifest{}, nil, err
	}
	m.Permissions = perms
	m.NodeKinds = kinds
	m.Views = views
	m.WorkflowSteps = steps
	m.AgentTools = tools
	m.Integrations = integrations
	m.BackgroundJobs = jobs
	m.DigestSHA256 = ""
	return m, bindings, nil
}

func hashPlugin(m Manifest, bindings map[string]string) (string, error) {
	binds := make([]stepBinding, 0, len(bindings))
	for key, perm := range bindings {
		binds = append(binds, stepBinding{Key: key, Permission: perm})
	}
	slices.SortFunc(binds, func(a, b stepBinding) int { return strings.Compare(a.Key, b.Key) })
	body, err := marshalCanonical(struct {
		ID             string         `json:"id"`
		Version        string         `json:"version"`
		Owner          string         `json:"owner"`
		Permissions    []string       `json:"permissions"`
		NodeKinds      []NodeKind     `json:"node_kinds"`
		Views          []View         `json:"views"`
		WorkflowSteps  []WorkflowStep `json:"workflow_steps"`
		AgentTools     []Capability   `json:"agent_tools"`
		Integrations   []Capability   `json:"integrations"`
		BackgroundJobs []Capability   `json:"background_jobs"`
		StepBindings   []stepBinding  `json:"step_permissions"`
	}{
		ID: m.ID, Version: m.Version, Owner: m.Owner, Permissions: m.Permissions,
		NodeKinds: m.NodeKinds, Views: m.Views, WorkflowSteps: m.WorkflowSteps,
		AgentTools: m.AgentTools, Integrations: m.Integrations, BackgroundJobs: m.BackgroundJobs,
		StepBindings: binds,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeNames(in []string, what string, known func(string) bool) ([]string, error) {
	if len(in) > maxItems {
		return nil, fmt.Errorf("plugins: too many %ss", what)
	}
	if in == nil {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, name := range in {
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("plugins: duplicate %s %q", what, name)
		}
		seen[name] = struct{}{}
		if known != nil && !known(name) {
			return nil, fmt.Errorf("plugins: unknown %s %q", what, name)
		}
		if known == nil && !validID(name) {
			return nil, fmt.Errorf("plugins: invalid %s %q", what, name)
		}
		out = append(out, name)
	}
	return out, nil
}

func normalizeKinds(in []NodeKind) ([]NodeKind, error) {
	if len(in) > maxItems {
		return nil, fmt.Errorf("plugins: too many node kinds")
	}
	out := make([]NodeKind, 0, len(in))
	seen := map[string]struct{}{}
	for _, kind := range in {
		if !validID(kind.Slug) {
			return nil, fmt.Errorf("plugins: invalid node kind %q", kind.Slug)
		}
		if _, ok := seen[kind.Slug]; ok {
			return nil, fmt.Errorf("plugins: duplicate node kind %q", kind.Slug)
		}
		seen[kind.Slug] = struct{}{}
		schema, err := canonicalSchema(kind.FieldSchema)
		if err != nil {
			return nil, fmt.Errorf("plugins: node kind %s: %w", kind.Slug, err)
		}
		children, err := normalizeNames(kind.AllowedChildKinds, "child kind", nil)
		if err != nil {
			return nil, err
		}
		slices.Sort(children)
		out = append(out, NodeKind{Slug: kind.Slug, FieldSchema: schema, AllowedChildKinds: children})
	}
	slices.SortFunc(out, func(a, b NodeKind) int { return strings.Compare(a.Slug, b.Slug) })
	return out, nil
}

func normalizeViews(in []View) ([]View, error) {
	if len(in) > maxItems {
		return nil, fmt.Errorf("plugins: too many views")
	}
	out := make([]View, 0, len(in))
	seen := map[string]struct{}{}
	for _, view := range in {
		if !validID(view.ID) {
			return nil, fmt.Errorf("plugins: invalid view %q", view.ID)
		}
		if _, ok := seen[view.ID]; ok {
			return nil, fmt.Errorf("plugins: duplicate view %q", view.ID)
		}
		seen[view.ID] = struct{}{}
		if len(view.Panels) == 0 {
			return nil, fmt.Errorf("plugins: view %s has no panels", view.ID)
		}
		panels, err := normalizeNames(view.Panels, "panel", nil)
		if err != nil {
			return nil, err
		}
		out = append(out, View{ID: view.ID, Panels: panels})
	}
	return out, nil
}

func normalizeSteps(in []WorkflowStep) ([]WorkflowStep, error) {
	if len(in) > maxItems {
		return nil, fmt.Errorf("plugins: too many workflow steps")
	}
	out := make([]WorkflowStep, 0, len(in))
	seen := map[string]struct{}{}
	for _, step := range in {
		if !validID(step.Key) {
			return nil, fmt.Errorf("plugins: invalid workflow step %q", step.Key)
		}
		if _, ok := seen[step.Key]; ok {
			return nil, fmt.Errorf("plugins: duplicate workflow step %q", step.Key)
		}
		seen[step.Key] = struct{}{}
		if len(step.Gates) == 0 {
			return nil, fmt.Errorf("plugins: workflow step %s has no gates", step.Key)
		}
		gates, err := normalizeNames(step.Gates, "gate", fence.KnownGate)
		if err != nil {
			return nil, err
		}
		out = append(out, WorkflowStep{Key: step.Key, Gates: gates})
	}
	return out, nil
}

func normalizeCaps(in []Capability, what string, ceiling map[string]struct{}) ([]Capability, error) {
	if len(in) > maxItems {
		return nil, fmt.Errorf("plugins: too many %ss", what)
	}
	out := make([]Capability, 0, len(in))
	seen := map[string]struct{}{}
	for _, cap := range in {
		if !validID(cap.ID) {
			return nil, fmt.Errorf("plugins: invalid %s %q", what, cap.ID)
		}
		if _, ok := seen[cap.ID]; ok {
			return nil, fmt.Errorf("plugins: duplicate %s %q", what, cap.ID)
		}
		seen[cap.ID] = struct{}{}
		if !fence.KnownPermission(cap.Permission) {
			return nil, fmt.Errorf("plugins: unknown permission %q", cap.Permission)
		}
		if _, ok := ceiling[cap.Permission]; !ok {
			return nil, fmt.Errorf("plugins: permission %q is outside the manifest ceiling", cap.Permission)
		}
		out = append(out, cap)
	}
	return out, nil
}

func normalizeBindings(steps []WorkflowStep, in map[string]string, ceiling map[string]struct{}) (map[string]string, error) {
	if len(in) != len(steps) {
		return nil, fmt.Errorf("plugins: step permissions do not match workflow steps")
	}
	out := make(map[string]string, len(steps))
	for _, step := range steps {
		perm, ok := in[step.Key]
		if !ok {
			return nil, fmt.Errorf("plugins: workflow step %s has no permission", step.Key)
		}
		if !fence.KnownPermission(perm) {
			return nil, fmt.Errorf("plugins: unknown permission %q", perm)
		}
		if _, allowed := ceiling[perm]; !allowed {
			return nil, fmt.Errorf("plugins: permission %q is outside the manifest ceiling", perm)
		}
		out[step.Key] = perm
	}
	return out, nil
}

func matchImpl(declared, present bool, what string) error {
	if declared != present {
		return fmt.Errorf("plugins: %s declaration does not match the implementation", what)
	}
	return nil
}

func canonicalSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("field_schema is required")
	}
	if len(raw) > maxSchema {
		return nil, fmt.Errorf("field_schema is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("field_schema must be JSON")
	}
	var extra any
	if err := dec.Decode(&extra); !errorsIsEOF(err) {
		return nil, fmt.Errorf("field_schema must be one JSON value")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("field_schema must be an object")
	}
	body, err := marshalCanonical(value)
	if err != nil {
		return nil, err
	}
	if len(body) > maxSchema {
		return nil, fmt.Errorf("field_schema is too large")
	}
	return body, nil
}

func schemaID(raw json.RawMessage) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	value, ok := obj["$id"]
	if !ok {
		return "", nil
	}
	id, ok := value.(string)
	if !ok || strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("plugins: $id must be a non-empty string")
	}
	return id, nil
}

func marshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func errorsIsEOF(err error) bool {
	return err == io.EOF
}

func validID(id string) bool {
	return idRe.MatchString(id) && len(id) <= maxID
}

func plainText(s string, max int) bool {
	if s == "" || len(s) > max || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func setOf(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		out[name] = struct{}{}
	}
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Registry holds compiled plugins. Register is safe for concurrent use.
// Seal it before serving; later Register calls fail.
type Registry struct {
	mu           sync.Mutex
	sealed       bool
	plugins      map[string]Plugin
	kinds        map[string]string
	schemaIDs    map[string]string
	views        map[string]string
	steps        map[string]string
	tools        map[string]string
	integrations map[string]string
	jobs         map[string]string
}

// NewRegistry returns an empty unsealed registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins:      map[string]Plugin{},
		kinds:        map[string]string{},
		schemaIDs:    map[string]string{},
		views:        map[string]string{},
		steps:        map[string]string{},
		tools:        map[string]string{},
		integrations: map[string]string{},
		jobs:         map[string]string{},
	}
}

// Register validates p and stores it. A digest mismatch, duplicate id, unknown
// permission, or schema collision is rejected.
func (r *Registry) Register(p Plugin) error {
	manifest, bindings, err := normalize(p)
	if err != nil {
		return err
	}
	sum, err := hashPlugin(manifest, bindings)
	if err != nil {
		return err
	}
	if p.Manifest.DigestSHA256 != sum {
		return fmt.Errorf("plugins: digest mismatch")
	}
	manifest.DigestSHA256 = sum
	ids, err := schemaIDsOf(manifest.NodeKinds)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("plugins: registry is sealed")
	}
	if _, ok := r.plugins[manifest.ID]; ok {
		return fmt.Errorf("plugins: duplicate id %q", manifest.ID)
	}
	kindIDs := idsOfKinds(manifest.NodeKinds)
	viewIDs := idsOfViews(manifest.Views)
	stepIDs := idsOfSteps(manifest.WorkflowSteps)
	toolIDs := idsOfCaps(manifest.AgentTools)
	integrationIDs := idsOfCaps(manifest.Integrations)
	jobIDs := idsOfCaps(manifest.BackgroundJobs)
	if err := collide(r.kinds, kindIDs, "node kind"); err != nil {
		return err
	}
	if err := collide(r.schemaIDs, ids, "field schema"); err != nil {
		return err
	}
	if err := collide(r.views, viewIDs, "view"); err != nil {
		return err
	}
	if err := collide(r.steps, stepIDs, "workflow step"); err != nil {
		return err
	}
	if err := collide(r.tools, toolIDs, "agent tool"); err != nil {
		return err
	}
	if err := collide(r.integrations, integrationIDs, "integration"); err != nil {
		return err
	}
	if err := collide(r.jobs, jobIDs, "background job"); err != nil {
		return err
	}
	occupy(r.kinds, kindIDs, manifest.ID)
	occupy(r.schemaIDs, ids, manifest.ID)
	occupy(r.views, viewIDs, manifest.ID)
	occupy(r.steps, stepIDs, manifest.ID)
	occupy(r.tools, toolIDs, manifest.ID)
	occupy(r.integrations, integrationIDs, manifest.ID)
	occupy(r.jobs, jobIDs, manifest.ID)
	stored := p
	stored.Manifest = manifest
	stored.StepPermissions = bindings
	r.plugins[manifest.ID] = stored.clone()
	return nil
}

// Seal freezes the registry.
func (r *Registry) Seal() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

func (r *Registry) isSealed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sealed
}

func (r *Registry) plugin(id string) (Plugin, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.plugins[id]
	if !ok {
		return Plugin{}, false
	}
	return p.clone(), true
}

func (r *Registry) list() []Plugin {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p.clone())
	}
	slices.SortFunc(out, func(a, b Plugin) int { return strings.Compare(a.Manifest.ID, b.Manifest.ID) })
	return out
}

func collide(index map[string]string, ids []string, what string) error {
	for _, id := range ids {
		if owner, ok := index[id]; ok {
			return fmt.Errorf("plugins: %s %q collides with %s", what, id, owner)
		}
	}
	return nil
}

func occupy(index map[string]string, ids []string, pluginID string) {
	for _, id := range ids {
		index[id] = pluginID
	}
}

func schemaIDsOf(kinds []NodeKind) ([]string, error) {
	var ids []string
	seen := map[string]struct{}{}
	for _, kind := range kinds {
		id, err := schemaID(kind.FieldSchema)
		if err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("plugins: field schema %q collides with %s", id, kind.Slug)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func idsOfKinds(kinds []NodeKind) []string {
	out := make([]string, len(kinds))
	for i, kind := range kinds {
		out[i] = kind.Slug
	}
	return out
}

func idsOfViews(views []View) []string {
	out := make([]string, len(views))
	for i, view := range views {
		out[i] = view.ID
	}
	return out
}

func idsOfSteps(steps []WorkflowStep) []string {
	out := make([]string, len(steps))
	for i, step := range steps {
		out[i] = step.Key
	}
	return out
}

func idsOfCaps(caps []Capability) []string {
	out := make([]string, len(caps))
	for i, cap := range caps {
		out[i] = cap.ID
	}
	return out
}

func (p Plugin) clone() Plugin {
	m := p.Manifest
	m.Permissions = cloneStrings(m.Permissions)
	m.NodeKinds = cloneKinds(m.NodeKinds)
	m.Views = cloneViews(m.Views)
	m.WorkflowSteps = cloneSteps(m.WorkflowSteps)
	m.AgentTools = cloneCaps(m.AgentTools)
	m.Integrations = cloneCaps(m.Integrations)
	m.BackgroundJobs = cloneCaps(m.BackgroundJobs)
	bindings := make(map[string]string, len(p.StepPermissions))
	for key, perm := range p.StepPermissions {
		bindings[key] = perm
	}
	p.Manifest = m
	p.StepPermissions = bindings
	return p
}

func cloneKinds(in []NodeKind) []NodeKind {
	out := make([]NodeKind, len(in))
	for i, kind := range in {
		kind.FieldSchema = json.RawMessage(append([]byte(nil), kind.FieldSchema...))
		kind.AllowedChildKinds = cloneStrings(kind.AllowedChildKinds)
		out[i] = kind
	}
	return out
}

func cloneViews(in []View) []View {
	out := make([]View, len(in))
	for i, view := range in {
		view.Panels = cloneStrings(view.Panels)
		out[i] = view
	}
	return out
}

func cloneSteps(in []WorkflowStep) []WorkflowStep {
	out := make([]WorkflowStep, len(in))
	for i, step := range in {
		step.Gates = cloneStrings(step.Gates)
		out[i] = step
	}
	return out
}

func cloneCaps(in []Capability) []Capability {
	if in == nil {
		return []Capability{}
	}
	out := make([]Capability, len(in))
	copy(out, in)
	return out
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

// normalizeDraft ensures list-shaped JSON fields serialize as [] rather than
// null. Partial or older responses must not crash list consumers.
func normalizeDraft(d *Draft) {
	if d == nil {
		return
	}
	if d.Requirements == nil {
		d.Requirements = []Requirement{}
	}
	for i := range d.Requirements {
		if d.Requirements[i].AcceptanceCriteria == nil {
			d.Requirements[i].AcceptanceCriteria = []string{}
		}
		if d.Requirements[i].ConstraintRefs == nil {
			d.Requirements[i].ConstraintRefs = []string{}
		}
	}
	if d.Constraints == nil {
		d.Constraints = []Constraint{}
	}
	if d.Unresolved == nil {
		d.Unresolved = []UnresolvedItem{}
	}
	if d.Selected.RequirementRefs == nil {
		d.Selected.RequirementRefs = []string{}
	}
	if d.Selected.ConstraintRefs == nil {
		d.Selected.ConstraintRefs = []string{}
	}
}

func normalizeBatch(b *Batch) {
	if b == nil {
		return
	}
	if b.Forecasts == nil {
		b.Forecasts = []Forecast{}
	}
	if b.Controls == nil {
		b.Controls = []ControlOption{}
	}
	if b.Progress.Stages == nil {
		b.Progress.Stages = []StageView{}
	}
	if b.Scope.RequirementRefs == nil {
		b.Scope.RequirementRefs = []string{}
	}
	if b.Scope.ConstraintRefs == nil {
		b.Scope.ConstraintRefs = []string{}
	}
	if b.Readiness != nil && b.Readiness.Checks == nil {
		b.Readiness.Checks = []ReadinessCheckView{}
	}
}

func normalizeWorkflow(w *Workflow) {
	if w == nil {
		return
	}
	if w.Batches == nil {
		w.Batches = []Batch{}
	}
	for i := range w.Batches {
		normalizeBatch(&w.Batches[i])
	}
	if w.Draft != nil {
		normalizeDraft(w.Draft)
	}
	if w.ActiveBatch != nil {
		normalizeBatch(w.ActiveBatch)
	}
	if w.Choices.ExecutionModes == nil {
		w.Choices.ExecutionModes = []string{}
	}
	if w.Choices.Runtimes == nil {
		w.Choices.Runtimes = []RuntimeChoice{}
	}
	for i := range w.Choices.Runtimes {
		r := &w.Choices.Runtimes[i]
		if r.Accounts == nil {
			r.Accounts = []AccountChoice{}
		}
		if r.Profiles == nil {
			r.Profiles = []ProfileChoice{}
		}
		if r.Workspaces == nil {
			r.Workspaces = []WorkspaceChoice{}
		}
	}
	if w.Readiness != nil && w.Readiness.Checks == nil {
		w.Readiness.Checks = []ReadinessCheckView{}
	}
}

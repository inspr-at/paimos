// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import "testing"

func TestCurrentRequiredJanusPrerequisiteCountRejectsEmptyOptionalAndWrongGeneration(t *testing.T) {
	for _, requirement := range []PrerequisiteRequirement{"", PrerequisiteOptional, PrerequisiteRequired} {
		t.Run(string(requirement), func(t *testing.T) {
			f := setupServiceFixture(t)
			refs := []Prerequisite{}
			if requirement != "" {
				reg, err := f.service.RegisterReporter(t.Context(), f.operator, f.deliveryKey, "projection-register", RegisterReporterRequest{APIKeyID: f.reporter.APIKeyID, ReporterClass: ReporterClassJanus, ReporterRole: ReporterRoleDependency, DependencyKey: "projection.check"})
				if err != nil {
					t.Fatal(err)
				}
				refs = append(refs, Prerequisite{DependencyKey: "projection.check", ReporterRegistrationID: reg.RegistrationID, Requirement: requirement})
			}
			if _, err := f.service.SealPrerequisites(t.Context(), f.operator, f.deliveryKey, "projection-seal", SealPrerequisitesRequest{StageKey: "deployment", ExecutionNumber: 1, ExpectedPlanRevision: 1, ExpectedAuthorityEpoch: 1, Prerequisites: refs}); err != nil {
				t.Fatal(err)
			}
			tx, err := f.database.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			count, err := CurrentRequiredJanusPrerequisiteCount(t.Context(), tx, f.deliveryID, f.attemptID, "deployment", 1, 1)
			want := 0
			if requirement == PrerequisiteRequired {
				want = 1
			}
			if err != nil || count != want {
				t.Fatalf("count=%d want=%d err=%v", count, want, err)
			}
			for _, scope := range [][4]int64{{0, f.attemptID, 1, 1}, {f.deliveryID, f.attemptID + 100, 1, 1}, {f.deliveryID, f.attemptID, 2, 1}, {f.deliveryID, f.attemptID, 1, 2}} {
				count, err := CurrentRequiredJanusPrerequisiteCount(t.Context(), tx, scope[0], scope[1], "deployment", scope[2], scope[3])
				if err != nil || count != 0 {
					t.Fatalf("wrong scope returned evidence: %v count=%d err=%v", scope, count, err)
				}
			}
		})
	}
}

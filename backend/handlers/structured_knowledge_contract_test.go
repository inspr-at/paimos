// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/inspr-at/paimos/backend/models"
	_ "modernc.org/sqlite"
)

func TestStructuredKnowledgeCompactHandlerGuardRejectsAgentTargetSession(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE product_sessions(
		product_session_id TEXT PRIMARY KEY,project_id INTEGER NOT NULL,target_kind TEXT NOT NULL,
		target_project_agent_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO product_sessions VALUES('11111111-1111-4111-8111-111111111111',42,'paimos',NULL)`,
		`INSERT INTO product_sessions VALUES('22222222-2222-4222-8222-222222222222',42,'project_agent',7)`,
		`INSERT INTO product_sessions VALUES('33333333-3333-4333-8333-333333333333',43,'paimos',NULL)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for name, test := range map[string]struct {
		id   string
		want bool
	}{
		"same-project Paimos": {"11111111-1111-4111-8111-111111111111", true},
		"same-project agent":  {"22222222-2222-4222-8222-222222222222", false},
		"foreign Paimos":      {"33333333-3333-4333-8333-333333333333", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := structuredKnowledgePaimosProductSessionTx(context.Background(), tx, 42, test.id)
			if err != nil || got != test.want {
				t.Fatalf("guard=%v err=%v, want %v", got, err, test.want)
			}
		})
	}
}

func TestStructuredKnowledgeLegacyAdoptionRequiresCanonicalEmptyMetadata(t *testing.T) {
	for name, metadata := range map[string]string{
		"inheritance":     `{"inherit":true}`,
		"related project": `{"related_project_url":"https://other.invalid"}`,
		"arbitrary":       `{"note":"second truth"}`,
		"noncanonical":    `{ }`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateStructuredKnowledgeLegacyAdoptionCandidate("legacy", "Legacy fact", "Short body", metadata); err == nil {
				t.Fatalf("metadata %q accepted", metadata)
			}
		})
	}
	for _, metadata := range []string{"", "{}"} {
		if err := validateStructuredKnowledgeLegacyAdoptionCandidate("legacy", "Legacy fact", "Short body", metadata); err != nil {
			t.Fatalf("canonical empty metadata %q rejected: %v", metadata, err)
		}
	}
}

func TestStructuredKnowledgePromotionAuthorizationConcealsForeignAndInstanceSources(t *testing.T) {
	policy := structuredKnowledgePromotionPolicyV1{
		ProjectToInstanceAdmin:       true,
		InstanceToTerminalSuperAdmin: true,
	}
	member := &models.User{ID: 1, Role: "member", Status: "active"}
	admin := &models.User{ID: 2, Role: "admin", Status: "active"}
	superAdmin := &models.User{ID: 3, Role: "super_admin", Status: "active"}

	if err := authorizeStructuredKnowledgePromotion(member, "project", "instance", false, policy); !errors.Is(err, errStructuredPromotionNotFound) {
		t.Fatalf("foreign project source error=%v, want concealed not found", err)
	}
	if err := authorizeStructuredKnowledgePromotion(member, "project", "instance", true, policy); !errors.Is(err, errStructuredPromotionForbidden) {
		t.Fatalf("same-project ordinary source error=%v, want role refusal", err)
	}
	if err := authorizeStructuredKnowledgePromotion(admin, "project", "instance", true, policy); err != nil {
		t.Fatalf("authorized project promotion rejected: %v", err)
	}
	for _, user := range []*models.User{member, admin} {
		if err := authorizeStructuredKnowledgePromotion(user, "instance", "kernel", false, policy); !errors.Is(err, errStructuredPromotionNotFound) {
			t.Fatalf("instance source leaked to role %q with error=%v", user.Role, err)
		}
	}
	if err := authorizeStructuredKnowledgePromotion(superAdmin, "instance", "vision", false, policy); err != nil {
		t.Fatalf("super-admin terminal promotion rejected: %v", err)
	}
}

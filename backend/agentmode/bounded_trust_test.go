// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
)

func TestLoadBoundedTrustUsesCallerSnapshotAndEnforcesBound(t *testing.T) {
	database := openAgentModeTestDB(t)
	projectResult, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Bounded trust','BTR')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	issueResult, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,904,'ticket','Bounded trust','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issueResult.LastInsertId()
	now := time.Now().UTC()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	facts, err := LoadBoundedTrust(context.Background(), database, tx, []int64{issueID, issueID}, now, delivery.DefaultFreshnessPolicy())
	if err != nil {
		t.Fatal(err)
	}
	fact, ok := facts[issueID]
	if !ok || fact.ObservedAt != now || fact.TrustRevision == "" || fact.ProgressTrusted || fact.ETATrusted || fact.Progress != nil || fact.ETA != nil {
		t.Fatalf("bounded trust fact=%+v present=%v", fact, ok)
	}
	tooMany := make([]int64, 101)
	for index := range tooMany {
		tooMany[index] = int64(index + 1)
	}
	if _, err := LoadBoundedTrust(context.Background(), database, tx, tooMany, now, delivery.DefaultFreshnessPolicy()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized bounded trust err=%v", err)
	}
}

package managedharness

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func TestBrowserControlCASAndAssignmentHistoryAuthority(t *testing.T) {
	project, _ := openManagedHarnessTestDB(t)
	ctx := context.Background()
	s := NewService(db.DB)
	var user int64
	if db.DB.QueryRow(`SELECT id FROM users WHERE username='harness-actor'`).Scan(&user) != nil {
		t.Fatal("fixture user")
	}
	credential := uuid.NewString()
	if _, e := db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), user, credential); e != nil {
		t.Fatal(e)
	}
	p, _ := auth.NewSessionPrincipal(credential, user, user, false)
	current, _, e := s.Register(ctx, RegisterInput{ProjectID: project, AgentName: "worker", Harness: "codex", Host: "fixture", SessionRef: uuid.NewString(), WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true}})
	if e != nil {
		t.Fatal(e)
	}
	req := BrowserControlRequest{ExpectedRevision: current.Revision, RequestKey: uuid.NewString()}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := s.RequestControlCAS(ctx, p, project, current.ID, "interrupt", req)
			if e != nil {
				t.Error(e)
			}
			ids <- out.Control.ID
		}()
	}
	wg.Wait()
	close(ids)
	var controlID string
	for id := range ids {
		if controlID != "" && id != controlID {
			t.Fatal("concurrent duplicate controls")
		}
		controlID = id
	}
	if _, e = s.RequestControlCAS(ctx, p, project, current.ID, "stop", BrowserControlRequest{ExpectedRevision: current.Revision + 1, RequestKey: uuid.NewString()}); e != ErrBrowserConflict {
		t.Fatal("stale selected revision accepted")
	}
	var count int
	if db.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_controls WHERE harness_session_id=?`, current.ID).Scan(&count) != nil || count != 1 {
		t.Fatal("stale request mutated ledger")
	}
	result, e := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,title,type) VALUES(?,1,'Fixture','ticket')`, project)
	if e != nil {
		t.Fatal(e)
	}
	ticket, _ := result.LastInsertId()
	for i := 0; i < 3; i++ {
		shape := "ship"
		if i == 1 {
			shape = "scout"
		}
		current, e = s.AssignBinding(ctx, BindingInput{ProjectID: project, SessionID: current.ID, ExpectedRevision: current.Revision, TicketID: &ticket, WorkShape: shape})
		if e != nil {
			t.Fatal(e)
		}
	}
	page, e := s.AssignmentHistory(ctx, p, project, current.ID, 0, 2)
	if e != nil || len(page.Events) != 2 || page.NextAfterRevision == nil {
		t.Fatal("history bound")
	}
	tail, e := s.AssignmentHistory(ctx, p, project, current.ID, *page.NextAfterRevision, 2)
	if e != nil || len(tail.Events) != 1 || tail.Events[0].AfterShape == nil || *tail.Events[0].AfterShape != "ship" {
		t.Fatal("history cursor")
	}
	if _, e = s.RequestControlCAS(ctx, p, project, current.ID, "interrupt", req); e != ErrBrowserConflict {
		t.Fatal("assignment revision not checked before replay")
	}
	// Same principal object, changed durable authorization: no middleware cache
	// can authorize the later transaction after permission revocation.
	if _, e = db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`, project, user); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RequestControlCAS(ctx, p, project, current.ID, "stop", BrowserControlRequest{ExpectedRevision: current.Revision, RequestKey: uuid.NewString()}); e != ErrBrowserUnavailable {
		t.Fatal("viewer mutated")
	}
	if _, e = s.AssignmentHistory(ctx, p, project, current.ID, 0, 1); e != nil {
		t.Fatal("viewer denied history")
	}
	if _, e = db.DB.Exec(`UPDATE project_members SET access_level='none' WHERE project_id=? AND user_id=?`, project, user); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{current.ID, uuid.NewString()} {
		if _, e = s.AssignmentHistory(ctx, p, project, id, 0, 1); e != ErrBrowserUnavailable {
			t.Fatal("history oracle")
		}
		if _, e = s.RequestControlCAS(ctx, p, project, id, "stop", req); e != ErrBrowserUnavailable {
			t.Fatal("control oracle")
		}
	}
	if _, e = db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, credential); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AssignmentHistory(ctx, p, project, current.ID, 0, 1); e != ErrBrowserUnavailable {
		t.Fatal("revoked session read")
	}
}

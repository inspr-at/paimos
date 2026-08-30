package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openM163Fixture(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m163.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := migrateThrough(database, 162); err != nil {
		t.Fatalf("create exact M162 fixture: %v", err)
	}
	return database
}

func TestMigration163KeepsPopulatedIssuesAndM161IdentityContract(t *testing.T) {
	database := openM163Fixture(t)
	user, err := database.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('m163-user','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M163 populated','M163')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	parent, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,1,'epic','Existing parent','backlog','medium')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	parentID, _ := parent.LastInsertId()
	child, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,2,'ticket','Existing child','backlog','medium')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := child.LastInsertId()
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, parentID, childID); err != nil {
		t.Fatal(err)
	}
	var activeAddressSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_harness_sessions_active_address'`).Scan(&activeAddressSQL); err != nil {
		t.Fatal(err)
	}

	if err := migrateThrough(database, 163); err != nil {
		t.Fatalf("M162 to M163 populated migration: %v", err)
	}
	var issueCount, edgeCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM issues WHERE project_id=?`, projectID).Scan(&issueCount); err != nil || issueCount != 2 {
		t.Fatalf("issues count=%d err=%v, want 2", issueCount, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM issue_relations WHERE source_id=? AND target_id=? AND type='parent'`, parentID, childID).Scan(&edgeCount); err != nil || edgeCount != 1 {
		t.Fatalf("parent edge count=%d err=%v, want 1", edgeCount, err)
	}
	var depth, afterIndexSQL string
	if err := database.QueryRow(`SELECT node_depth FROM projects WHERE id=?`, projectID).Scan(&depth); err != nil || depth != "nested" {
		t.Fatalf("node_depth=%q err=%v, want nested", depth, err)
	}
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_harness_sessions_active_address'`).Scan(&afterIndexSQL); err != nil || afterIndexSQL != activeAddressSQL {
		t.Fatalf("M161 active-address index changed: before=%q after=%q err=%v", activeAddressSQL, afterIndexSQL, err)
	}

	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		if _, err := database.Exec(`INSERT INTO product_sessions(
			product_session_id,project_id,target_kind,target_project_agent_id,title,created_by_user_id,updated_by_user_id)
			VALUES(?,?,'project_agent',?,'Independent product session',?,?)`, id, projectID, agentID, userID, userID); err != nil {
			t.Fatalf("same-agent product session %s rejected: %v", id, err)
		}
	}
	var productCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM product_sessions WHERE target_project_agent_id=?`, agentID).Scan(&productCount); err != nil || productCount != 2 {
		t.Fatalf("same-agent product sessions=%d err=%v, want 2", productCount, err)
	}
}

func TestMigration163EnforcesProductSessionProjectOwnership(t *testing.T) {
	database := openM163Fixture(t)
	if err := migrateThrough(database, 163); err != nil {
		t.Fatal(err)
	}
	user, _ := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m163-owner','x','member','active')`)
	userID, _ := user.LastInsertId()
	projectA, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('A','M3A')`)
	projectB, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('B','M3B')`)
	projectAID, _ := projectA.LastInsertId()
	projectBID, _ := projectB.LastInsertId()
	agent, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'other')`, projectBID)
	agentID, _ := agent.LastInsertId()
	node, _ := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,1,'ticket','Other node','backlog','medium')`, projectBID)
	nodeID, _ := node.LastInsertId()

	if _, err := database.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,title,created_by_user_id,updated_by_user_id)
		VALUES('33333333-3333-4333-8333-333333333333',?,'project_agent',?,'Wrong agent',?,?)`, projectAID, agentID, userID, userID); err == nil {
		t.Fatal("cross-project target agent accepted")
	}
	if _, err := database.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,node_id,title,created_by_user_id,updated_by_user_id)
		VALUES('44444444-4444-4444-8444-444444444444',?,'paimos',?,'Wrong node',?,?)`, projectAID, nodeID, userID, userID); err == nil {
		t.Fatal("cross-project node attachment accepted")
	}
}

func TestMigration163ParentGuardsAreDatabaseOwned(t *testing.T) {
	database := openM163Fixture(t)
	if err := migrateThrough(database, 163); err != nil {
		t.Fatal(err)
	}
	projectA, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Nested A','N3A')`)
	projectB, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Nested B','N3B')`)
	projectAID, _ := projectA.LastInsertId()
	projectBID, _ := projectB.LastInsertId()
	insertNode := func(projectID int64, number int) int64 {
		result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
			VALUES(?,?,'ticket','Node','backlog','medium')`, projectID, number)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	a := insertNode(projectAID, 1)
	b := insertNode(projectAID, 2)
	c := insertNode(projectAID, 3)
	other := insertNode(projectBID, 1)
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, b, c); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, c, a); err == nil {
		t.Fatal("raw SQL cycle accepted")
	}
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, other, a); err == nil {
		t.Fatal("raw SQL cross-project parent accepted")
	}
	if _, err := database.Exec(`UPDATE projects SET node_depth='1' WHERE id=?`, projectAID); err == nil {
		t.Fatal("depth 1 accepted while parent edges exist")
	}
	if _, err := database.Exec(`UPDATE projects SET node_depth='1' WHERE id=?`, projectBID); err != nil {
		t.Fatalf("empty project depth 1 rejected: %v", err)
	}
	newOther := insertNode(projectBID, 2)
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent')`, other, newOther); err == nil {
		t.Fatal("depth 1 parent edge accepted")
	}
}

func TestMigration163RejectsLegacyParentCyclesWithoutPartialSchema(t *testing.T) {
	database := openM163Fixture(t)
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Cycle','M3C')`)
	projectID, _ := project.LastInsertId()
	a, _ := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,1,'ticket','A','backlog','medium')`, projectID)
	b, _ := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,2,'ticket','B','backlog','medium')`, projectID)
	aID, _ := a.LastInsertId()
	bID, _ := b.LastInsertId()
	if _, err := database.Exec(`INSERT INTO issue_relations(source_id,target_id,type) VALUES(?,?,'parent'),(?,?,'parent')`, aID, bID, bID, aID); err != nil {
		t.Fatal(err)
	}
	err := migrateThrough(database, 163)
	if err == nil || !strings.Contains(err.Error(), "parent cycles block M163") {
		t.Fatalf("M163 cycle error=%v, want actionable precondition", err)
	}
	var version, tableCount, columnCount int
	if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil || version != 162 {
		t.Fatalf("schema version=%d err=%v, want 162", version, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='product_sessions'`).Scan(&tableCount); err != nil || tableCount != 0 {
		t.Fatalf("failed M163 product table count=%d err=%v", tableCount, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='node_depth'`).Scan(&columnCount); err != nil || columnCount != 0 {
		t.Fatalf("failed M163 node_depth column count=%d err=%v", columnCount, err)
	}
}

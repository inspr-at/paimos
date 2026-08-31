// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package knowledge857

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/brand"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	_ "modernc.org/sqlite"
)

type fixture struct {
	source, clone, report string
	policy                Policy
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	source := filepath.Join(root, "synthetic-857-backup")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(staging, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	createSyntheticSchema(t, database)
	insertIssue := func(id, number int64) {
		_, err := database.Exec(`INSERT INTO issues(id,issue_number,type,title,status,priority,slug,created_at,updated_at,content_rev)
			VALUES(?,?,'memory','same','backlog','medium','duplicate','clock-a','clock-a',?)`, id, number, id-1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO issue_control_revisions(issue_id,revision) VALUES(?,1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	insertIssue(10, 1)
	insertIssue(20, 2)
	// Freeze a true, non-empty WAL-safe trio while its synthetic writer is
	// quiescent. The repair source is the frozen copy, never this open database.
	for _, name := range trioNames {
		payload, readErr := os.ReadFile(filepath.Join(staging, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(payload) == 0 {
			t.Fatalf("synthetic WAL member %s is empty", name)
		}
		if err := os.WriteFile(filepath.Join(source, name), payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	fingerprints, err := fingerprintDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		source: source,
		clone:  filepath.Join(root, "clone"),
		report: filepath.Join(root, "report.json"),
		policy: Policy{SourceBasename: filepath.Base(source), Files: fingerprints, IssueCount: 2, KnowledgeCount: 2},
	}
}

func createSyntheticSchema(t *testing.T, database *sql.DB) {
	t.Helper()
	definitions := make([]string, 0, len(expectedIssueColumns))
	for _, column := range expectedIssueColumns {
		definition := `"` + column + `" TEXT`
		switch column {
		case "id":
			definition = `id INTEGER PRIMARY KEY`
		case "project_id", "user_id", "issue_number", "content_rev":
			definition = `"` + column + `" INTEGER`
		}
		definitions = append(definitions, definition)
	}
	statements := []string{
		`CREATE TABLE issues (` + strings.Join(definitions, ",") + `)`,
		`CREATE TABLE schema_versions(version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_versions(version) VALUES(161)`,
		`CREATE UNIQUE INDEX idx_issues_type_slug_project ON issues(project_id,type,slug) WHERE slug IS NOT NULL`,
		`CREATE TABLE issue_control_revisions(issue_id INTEGER PRIMARY KEY,revision INTEGER NOT NULL)`,
		`CREATE TRIGGER trg_issue_control_revision_on_delete AFTER DELETE ON issues BEGIN DELETE FROM issue_control_revisions WHERE issue_id=OLD.id; END`,
		`CREATE TABLE mutation_log(id INTEGER PRIMARY KEY,subject_type TEXT,subject_id INTEGER)`,
		`CREATE TABLE entity_embeddings(id INTEGER PRIMARY KEY,entity_type TEXT,entity_id INTEGER)`,
		`CREATE TABLE entity_relations(id INTEGER PRIMARY KEY,source_type TEXT,source_id INTEGER,target_type TEXT,target_id INTEGER)`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	for key := range durableNonFKIssueReferences {
		parts := strings.Split(key, "\x00")
		if _, err := database.Exec(`CREATE TABLE "` + parts[0] + `" (id INTEGER PRIMARY KEY,"` + parts[1] + `" INTEGER)`); err != nil {
			t.Fatal(err)
		}
	}
}

func runFixture(t *testing.T, f fixture, mutate func(*Options)) (Report, error) {
	t.Helper()
	opts := Options{SourceBackupDir: f.source, CloneDir: f.clone, ReportPath: f.report, Policy: f.policy}
	if mutate != nil {
		mutate(&opts)
	}
	return Run(context.Background(), opts)
}

func openFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(path, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), code+":") {
		t.Fatalf("error=%v, want code %s", err, code)
	}
}

func TestRepairHappyPathIsCloneOnlyDeterministicAndGuarded(t *testing.T) {
	f := newFixture(t)
	sourceBefore, err := fingerprintDirectory(f.source)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runFixture(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "clean" || report.DeletedRows != 1 || report.IssueCountAfter != 1 || report.KnowledgeAfter != 1 || report.Migration162State != "pending" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if !VerifyReportDigest(report) {
		t.Fatal("report digest does not verify")
	}
	cloneInfo, err := os.Stat(f.clone)
	if err != nil || cloneInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("clone permissions=%v err=%v", cloneInfo.Mode().Perm(), err)
	}
	for _, name := range trioNames {
		if report.CloneBefore[name].Size == 0 {
			t.Fatalf("copied WAL-safe member %s was not fingerprinted", name)
		}
	}
	dbInfo, err := os.Stat(filepath.Join(f.clone, databaseName))
	if err != nil || dbInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("clone database permissions err=%v", err)
	}
	var persisted Report
	payload, err := os.ReadFile(f.report)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	reportInfo, err := os.Stat(f.report)
	if err != nil || reportInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("report permissions err=%v", err)
	}
	if !VerifyReportDigest(persisted) {
		t.Fatal("persisted report digest does not verify")
	}
	sourceAfter, err := fingerprintDirectory(f.source)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameFingerprints(sourceBefore, sourceAfter, "test"); err != nil {
		t.Fatalf("source changed: %v", err)
	}
	database := openFixtureDB(t, f.clone)
	var ids string
	if err := database.QueryRow(`SELECT group_concat(id) FROM issues`).Scan(&ids); err != nil || ids != "10" {
		t.Fatalf("survivor ids=%q err=%v", ids, err)
	}
	var controls string
	if err := database.QueryRow(`SELECT group_concat(issue_id) FROM issue_control_revisions`).Scan(&controls); err != nil || controls != "10" {
		t.Fatalf("control projection=%q err=%v", controls, err)
	}
	clean, err := queryEmpty(context.Background(), database, "PRAGMA foreign_key_check")
	if err != nil || !clean {
		t.Fatalf("foreign keys clean=%v err=%v", clean, err)
	}
	if _, err := runFixture(t, f, nil); err == nil {
		t.Fatal("rerun unexpectedly succeeded")
	}
}

func TestRepairRefusesEveryUnsafeInputBoundary(t *testing.T) {
	t.Run("fingerprint mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.policy.Files[databaseName] = Fingerprint{Size: 1, SHA256: "bad"}
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "fingerprint_mismatch")
	})
	t.Run("count mismatch", func(t *testing.T) {
		f := newFixture(t)
		f.policy.IssueCount = 3
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "count_mismatch")
	})
	t.Run("M162 already applied", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `INSERT INTO schema_versions(version) VALUES(162)`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "migration_162_applied")
	})
	t.Run("issue schema drift", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `ALTER TABLE issues ADD COLUMN surprise TEXT`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "schema_drift")
	})
	t.Run("unknown issue reference", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `CREATE TABLE surprise(linked_issue_id INTEGER)`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "unknown_reference_surface")
	})
	t.Run("divergent null and empty", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `UPDATE issues SET category_metadata='' WHERE id=20`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "divergent_duplicate")
	})
	t.Run("mixed owner", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `UPDATE issues SET project_id=1,user_id=2 WHERE id=20`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "invalid_ownership")
	})
	t.Run("unsupported user type", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `UPDATE issues SET user_id=2,type='guideline' WHERE id=20`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "unsupported_user_type")
	})
	t.Run("unsupported user type without slug", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `UPDATE issues SET user_id=2,type='guideline',slug=NULL WHERE id=20`)
		f.policy.KnowledgeCount = 2
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "unsupported_user_type")
	})
	t.Run("nonknowledge collision", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `UPDATE issues SET type='ticket'`)
		f.policy.KnowledgeCount = 0
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "nonknowledge_collision")
	})
	t.Run("deleted collision", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `UPDATE issues SET deleted_at='gone' WHERE id=20`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "deleted_collision")
	})
	t.Run("existing clone", func(t *testing.T) {
		f := newFixture(t)
		if err := os.Mkdir(f.clone, 0700); err != nil {
			t.Fatal(err)
		}
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "clone_exists")
	})
	t.Run("same source clone", func(t *testing.T) {
		f := newFixture(t)
		f.clone = f.source
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "same_path")
	})
	t.Run("report equals clone", func(t *testing.T) {
		f := newFixture(t)
		f.report = f.clone
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "unsafe_report_path")
	})
	t.Run("source symlink", func(t *testing.T) {
		f := newFixture(t)
		link := filepath.Join(filepath.Dir(f.source), "link")
		if err := os.Symlink(f.source, link); err != nil {
			t.Fatal(err)
		}
		f.source = link
		f.policy.SourceBasename = "link"
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "unsafe_source")
	})
	t.Run("clone parent symlink", func(t *testing.T) {
		f := newFixture(t)
		realParent := filepath.Join(filepath.Dir(f.source), "real-parent")
		if err := os.Mkdir(realParent, 0700); err != nil {
			t.Fatal(err)
		}
		linkedParent := filepath.Join(filepath.Dir(f.source), "linked-parent")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatal(err)
		}
		f.clone = filepath.Join(linkedParent, "clone")
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "invalid_clone")
	})
	t.Run("extra backup member", func(t *testing.T) {
		f := newFixture(t)
		if err := os.WriteFile(filepath.Join(f.source, "extra"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "unsafe_backup_shape")
	})
	t.Run("source changes after copy", func(t *testing.T) {
		f := newFixture(t)
		_, err := runFixture(t, f, func(opts *Options) {
			opts.AfterCopy = func() error {
				file, openErr := os.OpenFile(filepath.Join(f.source, walName), os.O_APPEND|os.O_WRONLY, 0600)
				if openErr != nil {
					return openErr
				}
				defer file.Close()
				_, writeErr := file.Write([]byte{1})
				return writeErr
			}
		})
		assertCode(t, err, "source_changed")
	})
	t.Run("foreign key violation", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `PRAGMA foreign_keys=OFF; CREATE TABLE bad_fk(issue_id INTEGER REFERENCES issues(id)); INSERT INTO bad_fk(issue_id) VALUES(999999)`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "foreign_key_check")
	})
	t.Run("integrity violation", func(t *testing.T) {
		f := newFixture(t)
		mutateSource(t, &f, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET rootpage=999999 WHERE name='idx_issues_type_slug_project'; PRAGMA writable_schema=OFF`)
		_, err := runFixture(t, f, nil)
		assertCode(t, err, "integrity_check")
	})
}

func mutateSource(t *testing.T, f *fixture, statement string) {
	t.Helper()
	database := openFixtureDB(t, f.source)
	if _, err := database.Exec(statement); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	// SQLite may remove zero-byte sidecars; restore the exact required trio.
	for _, name := range []string{walName, shmName} {
		if _, err := os.Stat(filepath.Join(f.source, name)); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(filepath.Join(f.source, name), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fingerprints, err := fingerprintDirectory(f.source)
	if err != nil {
		t.Fatal(err)
	}
	f.policy.Files = fingerprints
}

func TestRepairRefusesAllKnownReferenceSurfaces(t *testing.T) {
	tests := []struct{ name, statement string }{
		{"declared foreign key", `CREATE TABLE child(issue_id INTEGER REFERENCES issues(id)); INSERT INTO child(issue_id) VALUES(20)`},
		{"mutation log", `INSERT INTO mutation_log(subject_type,subject_id) VALUES('issue',20)`},
		{"embedding", `INSERT INTO entity_embeddings(entity_type,entity_id) VALUES('issue',20)`},
		{"relation source", `INSERT INTO entity_relations(source_type,source_id,target_type,target_id) VALUES('issue',20,'project',1)`},
		{"relation target", `INSERT INTO entity_relations(source_type,source_id,target_type,target_id) VALUES('project',1,'issue',20)`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			mutateSource(t, &f, test.statement)
			_, err := runFixture(t, f, nil)
			assertCode(t, err, "referenced_duplicate")
		})
	}
	keys := make([]string, 0, len(durableNonFKIssueReferences))
	for key := range durableNonFKIssueReferences {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		t.Run(parts[0]+"_"+parts[1], func(t *testing.T) {
			f := newFixture(t)
			mutateSource(t, &f, `INSERT INTO "`+parts[0]+`"("`+parts[1]+`") VALUES(20)`)
			report, err := runFixture(t, f, nil)
			assertCode(t, err, "referenced_duplicate")
			if len(report.ReferenceSurfaces) == 0 {
				t.Fatal("refusal report omitted checked reference surfaces")
			}
		})
	}
}

func TestRepairRollsBackAfterFirstGuardedDelete(t *testing.T) {
	f := newFixture(t)
	_, err := runFixture(t, f, func(opts *Options) {
		opts.AfterDelete = func(deleted int) error {
			if deleted == 1 {
				return errors.New("stop")
			}
			return nil
		}
	})
	assertCode(t, err, "injected_failure")
	database := openFixtureDB(t, f.clone)
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
	var controls int
	if err := database.QueryRow(`SELECT COUNT(*) FROM issue_control_revisions`).Scan(&controls); err != nil || controls != 2 {
		t.Fatalf("rollback controls=%d err=%v", controls, err)
	}
}

func TestProductionPolicyIsExact(t *testing.T) {
	if ProductionPolicy.SourceBasename != "ppm-857-20260830-2130" || ProductionPolicy.IssueCount != 4881 || ProductionPolicy.KnowledgeCount != 326 {
		t.Fatalf("production lock changed: %+v", ProductionPolicy)
	}
	want := map[string]Fingerprint{
		databaseName: {196284416, "8531f27c41ce8824e737f35a909ae867a3f5247002dd62fcfcd6bef3d2521033"},
		walName:      {10102272, "cf8e76a8de55c34181ec33bfcb54bcfca72e92fb1e60813f18b2581474c80066"},
		shmName:      {32768, "554c72524009b4159b0016962e4e53db6e938483b1d2c4aee4872f23d5134150"},
	}
	if err := matchPolicy(ProductionPolicy.Files, want); err != nil {
		t.Fatal(err)
	}
}

func TestRepairedPopulatedM161CloneAppliesCurrentM162(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "synthetic-full-m161")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	originalBrand := brand.Default
	t.Cleanup(func() { brand.Default = originalBrand })
	brand.Default.DBFilename = databaseName
	t.Setenv("DATA_DIR", source)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := paimosdb.Open(); err != nil {
		t.Fatalf("create current fixture: %v", err)
	}
	database := paimosdb.DB
	for _, statement := range []string{
		`DROP INDEX idx_issues_knowledge_project_identity`,
		`DROP INDEX idx_issues_knowledge_user_identity`,
		`DROP INDEX idx_issues_knowledge_instance_identity`,
		`DROP TRIGGER trg_issues_scope_owner_insert`,
		`DROP TRIGGER trg_issues_scope_owner_update`,
		`DROP TRIGGER trg_issues_user_type_insert`,
		`DROP TRIGGER trg_issues_user_type_update`,
		`DELETE FROM schema_versions WHERE version=162`,
		`CREATE UNIQUE INDEX idx_issues_type_slug_project ON issues(type,slug,project_id) WHERE slug IS NOT NULL`,
		`INSERT INTO issues(project_id,user_id,issue_number,type,title,status,priority,slug) VALUES(NULL,NULL,900001,'memory','same','backlog','medium','repair-full')`,
		`INSERT INTO issues(project_id,user_id,issue_number,type,title,status,priority,slug) VALUES(NULL,NULL,900002,'memory','same','backlog','medium','repair-full')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	var issues, knowledge int64
	if err := database.QueryRow(`SELECT COUNT(*),coalesce(sum(CASE WHEN type IN ('memory','runbook','external_system','related_project','guideline') THEN 1 ELSE 0 END),0) FROM issues`).Scan(&issues, &knowledge); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{walName, shmName} {
		if err := os.WriteFile(filepath.Join(source, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fingerprints, err := fingerprintDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{source: source, clone: filepath.Join(root, "clone"), report: filepath.Join(root, "report.json"), policy: Policy{SourceBasename: filepath.Base(source), Files: fingerprints, IssueCount: issues, KnowledgeCount: knowledge}}
	if _, err := runFixture(t, f, nil); err != nil {
		t.Fatalf("repair full M161 fixture: %v", err)
	}

	t.Setenv("DATA_DIR", f.clone)
	if err := paimosdb.Open(); err != nil {
		t.Fatalf("normal db.Open did not apply M162: %v", err)
	}
	t.Cleanup(func() {
		if paimosdb.DB != nil {
			paimosdb.DB.Close()
		}
	})
	for _, name := range []string{"idx_issues_knowledge_project_identity", "idx_issues_knowledge_user_identity", "idx_issues_knowledge_instance_identity", "trg_issues_scope_owner_insert", "trg_issues_scope_owner_update", "trg_issues_user_type_insert", "trg_issues_user_type_update"} {
		var kind string
		if err := paimosdb.DB.QueryRow(`SELECT type FROM sqlite_master WHERE name=?`, name).Scan(&kind); err != nil {
			t.Fatalf("M162 object %s missing: %v", name, err)
		}
		if strings.HasPrefix(name, "idx_") {
			var definition string
			if err := paimosdb.DB.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&definition); err != nil || !strings.Contains(definition, "CREATE UNIQUE INDEX") || !strings.Contains(definition, "deleted_at IS NULL") {
				t.Fatalf("M162 index %s sql=%q err=%v", name, definition, err)
			}
		}
	}
	var version, matching int
	var finalIssues int64
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=162`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("M162 version count=%d err=%v", version, err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE slug='repair-full'`).Scan(&matching); err != nil || matching != 1 {
		t.Fatalf("post-M162 rows=%d err=%v", matching, err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&finalIssues); err != nil || finalIssues != issues-1 {
		t.Fatalf("post-M162 total=%d want=%d err=%v", finalIssues, issues-1, err)
	}
	clean, err := queryEmpty(context.Background(), paimosdb.DB, "PRAGMA foreign_key_check")
	if err != nil || !clean {
		t.Fatalf("post-M162 foreign keys clean=%v err=%v", clean, err)
	}
}

package lifecycleintents

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
)

var lifecycleSchema struct {
	once  sync.Once
	bytes []byte
	err   error
}

// setupWithClock owns an isolated DATA_DIR and the package-global db.DB, so
// these fixtures remain sequential. Cache only the fully migrated database
// before per-fixture authority seeding, never a live connection or a fixture's
// mutable database. Migration regressions still run their independent
// fresh/historical databases in db.
func openLifecycleFixtureDB(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("DATA_DIR"), brand.Default.DBFilename)
	lifecycleSchema.once.Do(func() {
		lifecycleSchema.err = db.Open()
		if db.DB != nil {
			// Closing all connections flushes the database before copying it;
			// no WAL or live database is copied into another fixture.
			if err := db.DB.Close(); lifecycleSchema.err == nil {
				lifecycleSchema.err = err
			}
			db.DB = nil
		}
		if lifecycleSchema.err == nil {
			lifecycleSchema.bytes, lifecycleSchema.err = os.ReadFile(path)
		}
	})
	if lifecycleSchema.err != nil {
		t.Fatalf("build lifecycle schema fixture: %v", lifecycleSchema.err)
	}
	if err := os.WriteFile(path, lifecycleSchema.bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep production connection hooks, pool settings and migration checks for
	// every private copy; only repeated historical table rebuilds are avoided.
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleFixtureCopiesDoNotShareDataOrSchema(t *testing.T) {
	var firstPath string
	var schemaVersion int
	t.Run("mutate first copy", func(t *testing.T) {
		f := setup(t)
		firstPath = os.Getenv("DATA_DIR")
		f.submit(t, f.request("repair"))
		if _, err := db.DB.Exec(`CREATE TABLE fixture_only_marker (value TEXT); INSERT INTO fixture_only_marker VALUES ('first fixture')`); err != nil {
			t.Fatal(err)
		}
		if err := db.DB.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&schemaVersion); err != nil {
			t.Fatal(err)
		}
	})
	if db.DB != nil {
		t.Fatal("fixture cleanup retained its connection")
	}
	t.Run("independent second copy", func(t *testing.T) {
		f := setup(t)
		if os.Getenv("DATA_DIR") == firstPath {
			t.Fatal("fixtures reused a mutable database directory")
		}
		var intents, marker, version, foreignKeys int
		for _, check := range []struct {
			query string
			got   *int
			want  int
		}{
			{`SELECT COUNT(*) FROM lifecycle_intents`, &intents, 0},
			{`SELECT COUNT(*) FROM sqlite_master WHERE name='fixture_only_marker'`, &marker, 0},
			{`SELECT MAX(version) FROM schema_versions`, &version, schemaVersion},
			{`PRAGMA foreign_keys`, &foreignKeys, 1},
		} {
			if err := db.DB.QueryRow(check.query).Scan(check.got); err != nil || *check.got != check.want {
				t.Fatalf("isolated schema check %q: got=%d want=%d err=%v", check.query, *check.got, check.want, err)
			}
		}
		// The second copy still supports the complete authorized service path.
		f.submit(t, f.request("repair"))
	})
}

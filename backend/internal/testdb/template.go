// Package testdb provides opt-in fixtures for sequential database tests.
// Only _test.go files may import this package; production uses db.Open directly.
package testdb

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
)

var template struct {
	sync.Once
	data []byte
	err  error
}

// Prepare copies an unseeded, fully migrated database into the caller's fresh
// DATA_DIR. Call db.Open afterward to retain production connection setup and
// the caller's PAIMOS_TEST_MODE behavior. Existing files are never overwritten.
// Like db.DB and process environment changes, this fixture requires sequential
// tests. Migration and populated-upgrade tests must continue using db.Open alone.
func Prepare(t testing.TB) {
	t.Helper()
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		t.Fatal("testdb.Prepare requires an explicit fresh DATA_DIR")
	}
	// Also reject calls from parallel tests, including after template creation.
	t.Setenv("DATA_DIR", dir)
	template.Do(func() { template.data, template.err = build(t) })
	if template.err != nil {
		t.Fatalf("build database template: %v", template.err)
	}
	path := filepath.Join(dir, brand.Default.DBFilename)
	// #nosec G304 G703 -- path joins the calling test's DATA_DIR from t.TempDir() with the fixed brand database filename; no request input.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create fixture database: %v", err)
	}
	_, writeErr := file.Write(template.data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("copy fixture database: write=%v close=%v", writeErr, closeErr)
	}
}

func build(t testing.TB) ([]byte, error) {
	dir, mode, previous := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE"), db.DB
	templateDir := t.TempDir()
	t.Setenv("DATA_DIR", templateDir)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	defer func() {
		t.Setenv("DATA_DIR", dir)
		t.Setenv("PAIMOS_TEST_MODE", mode)
		db.DB = previous
	}()
	if err := db.Open(); err != nil {
		if db.DB != nil && db.DB != previous {
			_ = db.DB.Close()
		}
		return nil, err
	}
	database := db.DB
	// Migrations may supply schema defaults, but no application fixtures or
	// vault operations run here. Checkpoint before reading one closed file;
	// MEMORY journal mode has no WAL frames and reports -1 for both counts.
	var busy, logFrames, checkpointed int
	err := database.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed)
	closeErr := database.Close()
	if err != nil {
		return nil, fmt.Errorf("checkpoint template: %w", err)
	}
	if busy != 0 || logFrames != checkpointed {
		return nil, fmt.Errorf("incomplete template checkpoint: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	path := filepath.Join(templateDir, brand.Default.DBFilename)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			return nil, fmt.Errorf("template sidecar %s remains after close: %v", suffix, err)
		}
	}
	// Keep immutable bytes for the test binary's lifetime, so the first test's
	// temporary directory can be removed without invalidating later fixtures.
	// #nosec G304 -- path joins the template's t.TempDir() with the fixed brand database filename; no request input.
	return os.ReadFile(path)
}

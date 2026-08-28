// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetentionMutationLogBatchesReleaseWriterBetweenCommits(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "retention.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(10)
	if _, err := database.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE mutation_log (
			id INTEGER PRIMARY KEY,
			parent_log_id INTEGER REFERENCES mutation_log(id) ON DELETE SET NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX idx_mutation_log_time ON mutation_log(created_at DESC)`,
		`CREATE INDEX idx_mutation_log_parent ON mutation_log(parent_log_id) WHERE parent_log_id IS NOT NULL`,
		`CREATE TABLE api_keys (id INTEGER PRIMARY KEY,last_used_at TEXT)`,
		`INSERT INTO api_keys(id) VALUES(1)`,
		`CREATE TABLE session_activity (
			id INTEGER PRIMARY KEY,session_id TEXT,user_id INTEGER,method TEXT,path TEXT,status_code INTEGER
		)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for id := 1; id <= 155; id++ {
		if _, err := database.Exec(`INSERT INTO mutation_log(id,created_at) VALUES(?,'2020-01-01')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO mutation_log(id,parent_log_id,created_at) VALUES(1001,1,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	firstBatchCommitted := make(chan struct{})
	resume := make(chan struct{})
	var yields atomic.Int32
	yield := func() {
		if yields.Add(1) == 1 {
			close(firstBatchCommitted)
			<-resume
		}
	}
	type result struct {
		removed int64
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		removed, sweepErr := sweepRetentionBatches(context.Background(), database, "mutation_log",
			"created_at < datetime('now', ?)", "DELETE FROM mutation_log", 50, yield, "-90 days")
		finished <- result{removed: removed, err: sweepErr}
	}()

	select {
	case <-firstBatchCommitted:
	case <-time.After(2 * time.Second):
		t.Fatal("first retention batch did not commit")
	}
	started := time.Now()
	if _, err := database.Exec(`UPDATE api_keys SET last_used_at=datetime('now') WHERE id=1`); err != nil {
		t.Fatalf("API-key usage write between retention batches: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO session_activity(session_id,user_id,method,path,status_code)
		VALUES('retention-proof',1,'POST','/api/issues',201)`); err != nil {
		t.Fatalf("session audit write between retention batches: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("request writes waited %s after a committed retention batch", elapsed)
	}
	close(resume)

	var sweep result
	select {
	case sweep = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded retention sweep did not finish")
	}
	if sweep.err != nil || sweep.removed != 155 {
		t.Fatalf("removed=%d err=%v", sweep.removed, sweep.err)
	}
	if yields.Load() < 4 {
		t.Fatalf("yielded %d times, want at least one per bounded batch", yields.Load())
	}
	var recent, linked, auditRows int
	if err := database.QueryRow(`SELECT COUNT(*),COUNT(parent_log_id) FROM mutation_log`).Scan(&recent, &linked); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM session_activity`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if recent != 1 || linked != 0 || auditRows != 1 {
		t.Fatalf("recent=%d linked=%d audit_rows=%d", recent, linked, auditRows)
	}
}

func TestRetentionBatchStopsWhenTriggerLeavesCandidateUnchanged(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "retention-trigger.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE TABLE retained (id INTEGER PRIMARY KEY, expired INTEGER NOT NULL)`,
		`INSERT INTO retained(id,expired) VALUES(1,1)`,
		`CREATE TRIGGER retain_candidate BEFORE DELETE ON retained BEGIN SELECT RAISE(IGNORE); END`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() {
		removed, sweepErr := sweepRetentionBatches(context.Background(), database, "retained",
			"expired=1", "DELETE FROM retained", 1, nil)
		if sweepErr == nil && removed != 0 {
			sweepErr = fmt.Errorf("removed=%d want 0", removed)
		}
		done <- sweepErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("zero-effect retention batch looped on an unchanged candidate")
	}
}

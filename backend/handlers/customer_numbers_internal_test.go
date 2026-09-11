// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestCustomerNumbersMonthlySequenceTimezoneAndRollback(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "numbers.db")+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE offer_sequences(key TEXT PRIMARY KEY,value INTEGER NOT NULL CHECK(value>0))`); err != nil {
		t.Fatal(err)
	}
	allocate := func(at time.Time, commit bool) string {
		t.Helper()
		tx, err := database.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		number, err := nextCustomerNumber(tx, at)
		if err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		return number
	}
	september := time.Date(2026, 9, 30, 21, 30, 0, 0, time.UTC)
	if got := allocate(september, true); got != "K26091" {
		t.Fatal(got)
	}
	if got := allocate(september, false); got != "K26092" {
		t.Fatal(got)
	}
	if got := allocate(september, true); got != "K26092" {
		t.Fatal("rolled-back allocation consumed a number", got)
	}
	for n := 3; n <= 10; n++ {
		if got, want := allocate(september, true), "K2609"+strconv.Itoa(n); got != want {
			t.Fatalf("got %s want %s", got, want)
		}
	}
	if got := allocate(september.Add(time.Hour), true); got != "K26101" {
		t.Fatal("Vienna month rollover", got)
	}
	if got := allocate(time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC), true); got != "K27011" {
		t.Fatal("Vienna year rollover", got)
	}

	var wg sync.WaitGroup
	results := make(chan string, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := database.Begin()
			if err != nil {
				failures <- err
				return
			}
			defer tx.Rollback()
			number, err := nextCustomerNumber(tx, september)
			if err == nil {
				err = tx.Commit()
			}
			if err != nil {
				failures <- err
				return
			}
			results <- number
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{}
	for number := range results {
		if seen[number] {
			t.Fatal("duplicate", number)
		}
		seen[number] = true
	}
	if len(seen) != 8 {
		t.Fatalf("only %d concurrent allocations", len(seen))
	}
}

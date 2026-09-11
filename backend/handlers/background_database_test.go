package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/inspr-at/paimos/backend/managedharness"
)

func TestEmbeddingWorkerDropsClosedDatabaseJobs(t *testing.T) {
	for _, closeDuringDebounce := range []bool{false, true} {
		t.Run(fmt.Sprintf("closeDuringDebounce=%t", closeDuringDebounce), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn, err := sql.Open("sqlite", ":memory:")
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if !closeDuringDebounce {
					if err := conn.Close(); err != nil {
						t.Fatal(err)
					}
				}

				// More than two seconds of obsolete debounces used to sit in
				// front of the next live job. Pending reruns must be dropped too.
				jobs := make([]projectContextEmbeddingJob, 16)
				projectContextEmbeddingState.mu.Lock()
				for i := range jobs {
					jobs[i] = projectContextEmbeddingJob{db: conn, projectID: int64(i + 1)}
					projectContextEmbeddingState.queued[jobs[i]] = true
					projectContextEmbeddingState.rerun[jobs[i]] = true
				}
				projectContextEmbeddingState.mu.Unlock()
				started := time.Now()
				done := make(chan struct{})
				go func() {
					for _, job := range jobs {
						runProjectContextEmbeddingJob(job)
					}
					close(done)
				}()
				if closeDuringDebounce {
					// Wait until the worker is blocked in its existing debounce.
					synctest.Wait()
					if err := conn.Close(); err != nil {
						t.Fatal(err)
					}
				}
				<-done
				wantElapsed := time.Duration(0)
				if closeDuringDebounce {
					wantElapsed = projectContextEmbeddingDebounce
				}
				if elapsed := time.Since(started); elapsed != wantElapsed {
					t.Errorf("closed jobs delayed the worker by %v, want %v", elapsed, wantElapsed)
				}
				projectContextEmbeddingState.mu.Lock()
				defer projectContextEmbeddingState.mu.Unlock()
				for _, job := range jobs {
					_, queued := projectContextEmbeddingState.queued[job]
					_, running := projectContextEmbeddingState.running[job]
					_, rerun := projectContextEmbeddingState.rerun[job]
					if queued || running || rerun {
						t.Errorf("closed job %d retained state: queued=%t running=%t rerun=%t", job.projectID, queued, running, rerun)
					}
				}
			})
		})
	}
}

func TestHarnessActivityReconcilerStopsOnClosedDatabase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			runHarnessActivityReconciler(managedharness.NewService(conn))
			close(done)
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("reconciler did not exit after database closure")
		}
	})
}

func TestIsClosedDatabaseError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"closed DB", errors.New("sql: database is closed"), true},
		{"wrapped closed DB", fmt.Errorf("index: %w", errors.New("sql: database is closed")), true},
		{"closed connection", sql.ErrConnDone, true},
		{"wrapped closed connection", fmt.Errorf("index: %w", sql.ErrConnDone), true},
		{"busy", errors.New("SQLITE_BUSY: database is locked"), false},
		{"other", errors.New("no such table: entity_embeddings"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isClosedDatabaseError(tc.err); got != tc.want {
				t.Errorf("isClosedDatabaseError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

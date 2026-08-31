// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"modernc.org/sqlite"
)

type sessionHomeZoomCountingDriver struct {
	inner driver.Driver
	count *atomic.Int64
}

func (wrapped sessionHomeZoomCountingDriver) Open(name string) (driver.Conn, error) {
	connection, err := wrapped.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &sessionHomeZoomCountingConnection{Conn: connection, count: wrapped.count}, nil
}

type sessionHomeZoomCountingConnection struct {
	driver.Conn
	count *atomic.Int64
}

func (connection *sessionHomeZoomCountingConnection) QueryContext(ctx context.Context, query string,
	args []driver.NamedValue) (driver.Rows, error) {
	connection.count.Add(1)
	return connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (connection *sessionHomeZoomCountingConnection) ExecContext(ctx context.Context, query string,
	args []driver.NamedValue) (driver.Result, error) {
	connection.count.Add(1)
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *sessionHomeZoomCountingConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return connection.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

var sessionHomeZoomCountingDriverSequence atomic.Int64

func TestSessionHomeZoomAuthorizationAndDataShareOneSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	// db.Open uses immediate transactions for production writes. Reopen this
	// isolated WAL database without that write preference so the test can place
	// a revocation precisely between two reads of the handler transaction.
	if err := db.DB.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "paimos.db"))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(4)
	db.DB = database
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})

	userResult, err := db.DB.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('zoom-snapshot-user','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	projectResult, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Zoom snapshot','ZSN','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'viewer')`, userID, projectID); err != nil {
		t.Fatal(err)
	}
	credentialID := "64000000-0000-4000-8000-000000000001"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('zoom-snapshot-session',?,datetime('now','+1 hour'),datetime('now'),?)`, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	productSessionID := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'paimos','Snapshot row','',?,?)`, productSessionID, projectID, userID, userID); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}

	makeRequest := func(hook func()) *http.Request {
		request := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/projects/%d/session-home/zoom/v1", projectID), nil)
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", fmt.Sprint(projectID))
		ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
		ctx = auth.WithPrincipal(ctx, principal)
		if hook != nil {
			ctx = context.WithValue(ctx, sessionHomeZoomAuthorizationHookKey{}, hook)
		}
		return request.WithContext(ctx)
	}

	authorized := make(chan struct{})
	continueRead := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		SessionHomeZoomV1(recorder, makeRequest(func() {
			close(authorized)
			<-continueRead
		}))
		done <- recorder
	}()
	<-authorized
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='none' WHERE user_id=? AND project_id=?`, userID, projectID); err != nil {
		close(continueRead)
		t.Fatal(err)
	}
	close(continueRead)
	response := <-done
	if response.Code != http.StatusOK {
		t.Fatalf("in-flight snapshot status=%d body=%s", response.Code, response.Body.String())
	}

	after := httptest.NewRecorder()
	SessionHomeZoomV1(after, makeRequest(nil))
	if after.Code != http.StatusNotFound || after.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("post-revocation status=%d cache=%q body=%s", after.Code, after.Header().Get("Cache-Control"), after.Body.String())
	}
}

func TestSessionHomeZoomSQLStatementCountIsIndependentOfSessionCount(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	userResult, err := db.DB.Exec(`INSERT INTO users(username,password,role,status)
		VALUES('zoom-count-admin','x','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	projectResult, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Zoom count','ZCT','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	credentialID := "64000000-0000-4000-8000-000000000002"
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('zoom-count-session',?,datetime('now','+1 hour'),datetime('now'),?)`, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	insertSession := func(index int) {
		t.Helper()
		if _, err := db.DB.Exec(`INSERT INTO product_sessions(
			product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id)
			VALUES(?,?,'paimos',?,'',?,?)`, uuid.NewString(), projectID, fmt.Sprintf("Count %04d", index), userID, userID); err != nil {
			t.Fatal(err)
		}
	}
	insertSession(0)
	if err := db.DB.Close(); err != nil {
		t.Fatal(err)
	}

	counter := &atomic.Int64{}
	driverName := fmt.Sprintf("session-home-zoom-counting-sqlite-%d", sessionHomeZoomCountingDriverSequence.Add(1))
	sql.Register(driverName, sessionHomeZoomCountingDriver{inner: &sqlite.Driver{}, count: counter})
	countingDB, err := sql.Open(driverName, filepath.Join(dataDir, "paimos.db"))
	if err != nil {
		t.Fatal(err)
	}
	countingDB.SetMaxOpenConns(1)
	db.DB = countingDB
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	principal, err := auth.NewSessionPrincipal(credentialID, userID, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	requestSnapshot := func() (int64, models.SessionHomeZoomSnapshot) {
		request := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/projects/%d/session-home/zoom/v1", projectID), nil)
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", fmt.Sprint(projectID))
		ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
		request = request.WithContext(auth.WithPrincipal(ctx, principal))
		recorder := httptest.NewRecorder()
		counter.Store(0)
		SessionHomeZoomV1(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var snapshot models.SessionHomeZoomSnapshot
		if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		return counter.Load(), snapshot
	}

	oneCount, one := requestSnapshot()
	for index := 1; index <= 1000; index++ {
		insertSession(index)
	}
	manyCount, many := requestSnapshot()
	if oneCount != manyCount || oneCount != 4 {
		t.Fatalf("SQL count one=%d many=%d", oneCount, manyCount)
	}
	if one.Totals.Sessions != 1 || many.Totals.Sessions != 1001 || len(many.Sessions) != 10 || !many.SampleTruncated {
		t.Fatalf("bounded count snapshots one=%+v many=%+v", one, many)
	}
}

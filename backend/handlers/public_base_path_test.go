// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/publicbase"
)

func TestNativePrefixMountsHealthAndRejectsOutside(t *testing.T) {
	prefix, err := publicbase.Parse("/paimos")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(publicbase.RejectOutsideAndStrip(prefix)(buildRouter()))
	t.Cleanup(srv.Close)

	get := func(path string) *http.Response {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	outside := get("/api/health")
	body, _ := io.ReadAll(outside.Body)
	outside.Body.Close()
	if outside.StatusCode != http.StatusNotFound {
		t.Fatalf("root /api/health status=%d body=%s want 404", outside.StatusCode, body)
	}

	near := get("/paimos2/api/health")
	near.Body.Close()
	if near.StatusCode != http.StatusNotFound {
		t.Fatalf("near-miss /paimos2/api/health status=%d want 404", near.StatusCode)
	}

	ok := get("/paimos/api/health")
	okBody, _ := io.ReadAll(ok.Body)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("prefixed /paimos/api/health status=%d body=%s", ok.StatusCode, okBody)
	}
}

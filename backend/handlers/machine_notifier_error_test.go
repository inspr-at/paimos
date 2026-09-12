// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMachineNotifierUnexpectedErrorIsSanitized(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/machine-notifier/messages", nil)
	writeMachineNotifierError(recorder, request, errors.New("private database detail"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "private database detail") {
		t.Fatal("unexpected internal detail reached HTTP response")
	}
}

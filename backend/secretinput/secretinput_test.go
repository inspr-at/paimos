// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package secretinput

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptionalFileWinsAndRemovesExactlyOneLineEnding(t *testing.T) {
	t.Setenv("TEST_SECRET", "environment-secret")
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(" file-secret \r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_FILE", path)

	got, err := Optional("TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if got != " file-secret " {
		t.Fatalf("Optional() = %q, want preserved whitespace", got)
	}
}

func TestOptionalPreservesSecondTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("file-secret\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_FILE", path)

	got, err := Optional("TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if got != "file-secret\n" {
		t.Fatalf("Optional() = %q, want exactly one newline removed", got)
	}
}

func TestOptionalUnreadableFileFailsClosedWithoutPathOrFallback(t *testing.T) {
	t.Setenv("TEST_SECRET", "environment-secret")
	missing := filepath.Join(t.TempDir(), "missing-secret")
	t.Setenv("TEST_SECRET_FILE", missing)

	_, err := Optional("TEST_SECRET")
	if err == nil {
		t.Fatal("Optional() succeeded with unreadable TEST_SECRET_FILE")
	}
	var inputErr *Error
	if !errors.As(err, &inputErr) || inputErr.Variable != "TEST_SECRET_FILE" {
		t.Fatalf("Optional() error = %v, want TEST_SECRET_FILE error", err)
	}
	for _, forbidden := range []string{missing, "environment-secret"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed forbidden material %q: %v", forbidden, err)
		}
	}
}

func TestOptionalEmptyFileVariableFailsClosed(t *testing.T) {
	t.Setenv("TEST_SECRET", "environment-secret")
	t.Setenv("TEST_SECRET_FILE", "")

	_, err := Optional("TEST_SECRET")
	var inputErr *Error
	if !errors.As(err, &inputErr) || inputErr.Variable != "TEST_SECRET_FILE" || inputErr.Kind != "unreadable" {
		t.Fatalf("Optional() error = %v, want unreadable TEST_SECRET_FILE error", err)
	}
}

func TestOptionalEmptyFileFailsSpecifically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_FILE", path)

	_, err := Optional("TEST_SECRET")
	var inputErr *Error
	if !errors.As(err, &inputErr) || inputErr.Kind != "empty" {
		t.Fatalf("Optional() error = %v, want empty-file error", err)
	}
}

func TestOptionalDirectEnvironmentCompatibility(t *testing.T) {
	t.Setenv("TEST_SECRET", " environment-secret ")
	if got, err := Optional("TEST_SECRET"); err != nil || got != " environment-secret " {
		t.Fatalf("Optional() = %q, %v", got, err)
	}
}

func TestValidateRejectsConfiguredUnreadableFile(t *testing.T) {
	t.Setenv("SECOND_SECRET_FILE", filepath.Join(t.TempDir(), "missing-secret"))
	if err := Validate("FIRST_SECRET", "SECOND_SECRET"); err == nil || !strings.Contains(err.Error(), "SECOND_SECRET_FILE") {
		t.Fatalf("Validate() error = %v, want specific SECOND_SECRET_FILE failure", err)
	}
}

func TestFileModeKeepsSecretOutOfProcessEnvironment(t *testing.T) {
	const secret = "process-environment-canary"
	t.Setenv("TEST_SECRET", "")
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_FILE", path)

	got, err := Optional("TEST_SECRET")
	if err != nil || got != secret {
		t.Fatalf("Optional() = %q, %v", got, err)
	}
	for _, entry := range os.Environ() {
		if strings.Contains(entry, secret) {
			t.Fatalf("secret value appeared in process environment")
		}
	}
}

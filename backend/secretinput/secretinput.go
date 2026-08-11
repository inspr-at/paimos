// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package secretinput loads secret configuration directly from the
// environment or from an operator-managed runtime file.
package secretinput

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const maxSecretBytes = 1 << 20

// Error describes a secret-input configuration failure without exposing the
// configured path or any secret material.
type Error struct {
	Variable string
	Kind     string
}

func (e *Error) Error() string {
	switch e.Kind {
	case "empty":
		return fmt.Sprintf("%s contains an empty secret", e.Variable)
	default:
		return fmt.Sprintf("%s does not name a readable file", e.Variable)
	}
}

// Optional loads NAME_FILE when it is configured, otherwise NAME. File input
// wins even when the direct variable is also present. Exactly one trailing LF
// or CRLF line ending is removed; every other byte is preserved.
func Optional(name string) (string, error) {
	fileVariable := name + "_FILE"
	path, configured := os.LookupEnv(fileVariable)
	if !configured {
		return os.Getenv(name), nil
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return "", &Error{Variable: fileVariable, Kind: "unreadable"}
	}
	file, err := os.Open(path) // #nosec G304 -- path is explicit operator configuration.
	if err != nil {
		return "", &Error{Variable: fileVariable, Kind: "unreadable"}
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
	if err != nil || len(contents) > maxSecretBytes {
		return "", &Error{Variable: fileVariable, Kind: "unreadable"}
	}
	contents = withoutOneTrailingLineEnding(contents)
	if len(contents) == 0 {
		return "", &Error{Variable: fileVariable, Kind: "empty"}
	}
	return string(contents), nil
}

// Validate ensures all configured runtime secret files are readable before
// the server opens its database or starts listening.
func Validate(names ...string) error {
	for _, name := range names {
		if _, err := Optional(name); err != nil {
			return err
		}
	}
	return nil
}

func withoutOneTrailingLineEnding(value []byte) []byte {
	if len(value) == 0 || value[len(value)-1] != '\n' {
		return value
	}
	value = value[:len(value)-1]
	if len(value) > 0 && value[len(value)-1] == '\r' {
		value = value[:len(value)-1]
	}
	return value
}

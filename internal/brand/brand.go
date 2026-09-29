// SPDX-License-Identifier: AGPL-3.0-only

// Package brand is the product's names, from brand.json (schema inspr.brand.v1).
// The file at the repository root is embedded in the binary; a deployment may
// replace it with AEON_BRAND_FILE, which is validated at startup (a bad file
// stops the server). The web app reads the brand from GET /api/version and uses
// it on every visible surface.
//
//	{
//	  "schema": "inspr.brand.v1",
//	  "product": "PAIMOS",        the product family
//	  "generation": "7",          a label ("PAIMOS 7"), never a version
//	  "release_name": "AEON",     the name of this generation
//	  "wordmark": "PAIMOS AEON",  the lettering in the header, footer and sign-in
//	  "short_name": "AEON"        where space is short (titles, "What's new in …")
//	}
//
// Versions stay calendar versions (inspr-calver-3); nothing here is one.
package brand

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	aeon "github.com/inspr-at/paimos"
)

// Schema is the identifier every brand file carries.
const Schema = "inspr.brand.v1"

// EnvFile names the environment variable that points at a replacement brand file.
const EnvFile = "AEON_BRAND_FILE"

// Brand is the product's names.
type Brand struct {
	Schema      string `json:"schema"`
	Product     string `json:"product"`
	Generation  string `json:"generation"`
	ReleaseName string `json:"release_name"`
	Wordmark    string `json:"wordmark"`
	ShortName   string `json:"short_name"`
}

// Parse reads and validates a brand file.
func Parse(raw []byte) (Brand, error) {
	var b Brand
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return Brand{}, fmt.Errorf("brand: %w", err)
	}
	return b, b.Validate()
}

// Validate checks the schema and that every name is present and printable.
func (b Brand) Validate() error {
	if b.Schema != Schema {
		return fmt.Errorf("brand: schema %q, want %q", b.Schema, Schema)
	}
	for field, value := range map[string]string{"product": b.Product, "generation": b.Generation, "release_name": b.ReleaseName, "wordmark": b.Wordmark, "short_name": b.ShortName} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("brand: %s is required", field)
		}
		if value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > 48 || strings.ContainsAny(value, "\n\r\t<>") {
			return fmt.Errorf("brand: %s must be one trimmed line of at most 48 characters without < or >", field)
		}
	}
	return nil
}

// Default is the embedded brand.json. It is validated by tests, so it cannot fail at runtime.
func Default() Brand {
	b, err := Parse(aeon.BrandJSON)
	if err != nil {
		panic(err)
	}
	return b
}

// Load returns the brand for this process: the file named by AEON_BRAND_FILE when
// it is set (an error when it is missing or invalid), else the embedded default.
func Load() (Brand, error) {
	path := strings.TrimSpace(os.Getenv(EnvFile))
	if path == "" {
		return Default(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Brand{}, fmt.Errorf("brand: %s: %w", EnvFile, errors.Unwrap(err))
	}
	b, err := Parse(raw)
	if err != nil {
		return Brand{}, fmt.Errorf("%s=%s: %w", EnvFile, path, err)
	}
	return b, nil
}

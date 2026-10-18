// SPDX-License-Identifier: AGPL-3.0-only

// Package fieldschema validates node fields with exact JSON numbers.
package fieldschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"
)

// schema keywords this package enforces. Anything else is rejected when a
// kind is saved so a schema cannot silently skip a constraint.
var schemaAnnotations = map[string]bool{
	"title": true, "description": true, "default": true, "examples": true,
	"deprecated": true, "$schema": true, "$id": true, "$comment": true,
	"$defs": true, "definitions": true,
}

// Schema is the compiled field constraint tree.
type Schema struct {
	types            []string
	props            map[string]*Schema
	required         []string
	additionalSet    bool
	additionalAllow  bool
	additionalSchema *Schema
	items            *Schema
	enum             []any
	hasEnum          bool
	constVal         any
	hasConst         bool
	minLength        *int
	maxLength        *int
	pattern          *regexp.Regexp
	minimum          *big.Rat
	maximum          *big.Rat
	exclusiveMin     *big.Rat
	exclusiveMax     *big.Rat
	minItems         *int
	maxItems         *int
	minProps         *int
	maxProps         *int
}

// Compile checks the supported field-schema keywords and preserves exact bounds.
func Compile(raw json.RawMessage) (*Schema, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("field_schema is required")
	}
	v, err := Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("field_schema must be a JSON object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("field_schema must be a JSON object")
	}
	s, err := compileObject(obj)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func compileObject(obj map[string]any) (*Schema, error) {
	s := &Schema{}
	for key, val := range obj {
		if schemaAnnotations[key] {
			continue
		}
		switch key {
		case "type":
			types, err := schemaTypes(val)
			if err != nil {
				return nil, err
			}
			s.types = types
		case "properties":
			props, ok := val.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("properties must be an object")
			}
			s.props = map[string]*Schema{}
			for name, child := range props {
				childObj, ok := child.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("property %s schema must be an object", name)
				}
				compiled, err := compileObject(childObj)
				if err != nil {
					return nil, err
				}
				s.props[name] = compiled
			}
		case "required":
			names, err := stringArray(val)
			if err != nil {
				return nil, fmt.Errorf("required must be an array of strings")
			}
			s.required = names
		case "additionalProperties":
			s.additionalSet = true
			switch child := val.(type) {
			case bool:
				s.additionalAllow = child
			case map[string]any:
				compiled, err := compileObject(child)
				if err != nil {
					return nil, err
				}
				s.additionalSchema = compiled
			default:
				return nil, fmt.Errorf("additionalProperties must be a boolean or schema")
			}
		case "items":
			child, ok := val.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("items must be a schema object")
			}
			compiled, err := compileObject(child)
			if err != nil {
				return nil, err
			}
			s.items = compiled
		case "enum":
			list, ok := val.([]any)
			if !ok || len(list) == 0 {
				return nil, fmt.Errorf("enum must be a non-empty array")
			}
			s.enum = list
			s.hasEnum = true
		case "const":
			s.constVal = val
			s.hasConst = true
		case "minLength":
			n, err := schemaInt(val)
			if err != nil {
				return nil, fmt.Errorf("minLength must be an integer")
			}
			s.minLength = &n
		case "maxLength":
			n, err := schemaInt(val)
			if err != nil {
				return nil, fmt.Errorf("maxLength must be an integer")
			}
			s.maxLength = &n
		case "pattern":
			text, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("pattern must be a string")
			}
			re, err := regexp.Compile(text)
			if err != nil {
				return nil, fmt.Errorf("pattern is invalid")
			}
			s.pattern = re
		case "minimum":
			r, err := schemaRat(val)
			if err != nil {
				return nil, fmt.Errorf("minimum must be a number")
			}
			s.minimum = r
		case "maximum":
			r, err := schemaRat(val)
			if err != nil {
				return nil, fmt.Errorf("maximum must be a number")
			}
			s.maximum = r
		case "exclusiveMinimum":
			r, err := schemaRat(val)
			if err != nil {
				return nil, fmt.Errorf("exclusiveMinimum must be a number")
			}
			s.exclusiveMin = r
		case "exclusiveMaximum":
			r, err := schemaRat(val)
			if err != nil {
				return nil, fmt.Errorf("exclusiveMaximum must be a number")
			}
			s.exclusiveMax = r
		case "minItems":
			n, err := schemaInt(val)
			if err != nil {
				return nil, fmt.Errorf("minItems must be an integer")
			}
			s.minItems = &n
		case "maxItems":
			n, err := schemaInt(val)
			if err != nil {
				return nil, fmt.Errorf("maxItems must be an integer")
			}
			s.maxItems = &n
		case "minProperties":
			n, err := schemaInt(val)
			if err != nil {
				return nil, fmt.Errorf("minProperties must be an integer")
			}
			s.minProps = &n
		case "maxProperties":
			n, err := schemaInt(val)
			if err != nil {
				return nil, fmt.Errorf("maxProperties must be an integer")
			}
			s.maxProps = &n
		case "states":
			// Workflow states are not field constraints. The raw schema is stored,
			// and project counts read states[].category from it.
			if err := validateStateCatalog(val); err != nil {
				return nil, err
			}
		case "issue_family":
			// Marks a tenant kind as an issue kind. Absent means the seeded
			// issue kinds, and kinds that use their icons, are the family.
			if _, ok := val.(bool); !ok {
				return nil, fmt.Errorf("issue_family must be a boolean")
			}
		default:
			return nil, fmt.Errorf("unsupported field_schema keyword %q", key)
		}
	}
	return s, nil
}

// Validate checks a decoded JSON value; numbers must be json.Number.
func (s *Schema) Validate(v any) error {
	return s.ValidateAt("fields", v)
}

// ValidateAt checks a value at a caller-supplied field path.
func (s *Schema) ValidateAt(path string, v any) error {
	if len(s.types) > 0 && !typeMatches(s.types, v) {
		return fmt.Errorf("%s has the wrong type", path)
	}
	if s.hasConst && !jsonEqual(v, s.constVal) {
		return fmt.Errorf("%s must equal the schema const", path)
	}
	if s.hasEnum && !enumContains(s.enum, v) {
		return fmt.Errorf("%s is not an allowed value", path)
	}
	switch val := v.(type) {
	case map[string]any:
		if s.minProps != nil && len(val) < *s.minProps {
			return fmt.Errorf("%s has too few properties", path)
		}
		if s.maxProps != nil && len(val) > *s.maxProps {
			return fmt.Errorf("%s has too many properties", path)
		}
		for _, name := range s.required {
			if _, ok := val[name]; !ok {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		for name, child := range val {
			if sub, ok := s.props[name]; ok {
				if err := sub.ValidateAt(path+"."+name, child); err != nil {
					return err
				}
				continue
			}
			if !s.additionalSet || s.additionalAllow {
				continue
			}
			if s.additionalSchema != nil {
				if err := s.additionalSchema.ValidateAt(path+"."+name, child); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("%s.%s is not allowed", path, name)
		}
	case []any:
		if s.minItems != nil && len(val) < *s.minItems {
			return fmt.Errorf("%s has too few items", path)
		}
		if s.maxItems != nil && len(val) > *s.maxItems {
			return fmt.Errorf("%s has too many items", path)
		}
		if s.items != nil {
			for i, child := range val {
				if err := s.items.ValidateAt(fmt.Sprintf("%s[%d]", path, i), child); err != nil {
					return err
				}
			}
		}
	case string:
		n := utf8.RuneCountInString(val)
		if s.minLength != nil && n < *s.minLength {
			return fmt.Errorf("%s is too short", path)
		}
		if s.maxLength != nil && n > *s.maxLength {
			return fmt.Errorf("%s is too long", path)
		}
		if s.pattern != nil && !s.pattern.MatchString(val) {
			return fmt.Errorf("%s does not match the schema pattern", path)
		}
	case json.Number:
		r, ok := new(big.Rat).SetString(val.String())
		if !ok {
			return fmt.Errorf("%s is not a number", path)
		}
		if s.minimum != nil && r.Cmp(s.minimum) < 0 {
			return fmt.Errorf("%s is below the minimum", path)
		}
		if s.maximum != nil && r.Cmp(s.maximum) > 0 {
			return fmt.Errorf("%s is above the maximum", path)
		}
		if s.exclusiveMin != nil && r.Cmp(s.exclusiveMin) <= 0 {
			return fmt.Errorf("%s is below the minimum", path)
		}
		if s.exclusiveMax != nil && r.Cmp(s.exclusiveMax) >= 0 {
			return fmt.Errorf("%s is above the maximum", path)
		}
	}
	return nil
}

func typeMatches(types []string, v any) bool {
	for _, t := range types {
		if typeMatch(t, v) {
			return true
		}
	}
	return false
}

func typeMatch(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		r, ok := new(big.Rat).SetString(n.String())
		return ok && r.IsInt()
	default:
		return false
	}
}

func enumContains(list []any, v any) bool {
	for _, item := range list {
		if jsonEqual(v, item) {
			return true
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	if an, aok := a.(json.Number); aok {
		if bn, bok := b.(json.Number); bok {
			ar, aok := new(big.Rat).SetString(an.String())
			br, bok := new(big.Rat).SetString(bn.String())
			return aok && bok && ar.Cmp(br) == 0
		}
	}
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	return aerr == nil && berr == nil && bytes.Equal(ab, bb)
}

// Decode retains JSON numbers rather than converting them to float64.
func Decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, err
	}
	return v, nil
}

func schemaTypes(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		if !knownType(t) {
			return nil, fmt.Errorf("unknown schema type %q", t)
		}
		return []string{t}, nil
	case []any:
		if len(t) == 0 {
			return nil, fmt.Errorf("type must not be empty")
		}
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok || !knownType(s) {
				return nil, fmt.Errorf("type must be a string or array of type names")
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("type must be a string or array of type names")
	}
}

func knownType(s string) bool {
	switch s {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func stringArray(v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("not an array")
	}
	out := make([]string, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("not a string")
		}
		out[i] = s
	}
	return out, nil
}

func schemaInt(v any) (int, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("not a number")
	}
	i, err := n.Int64()
	if err != nil || i < 0 {
		return 0, fmt.Errorf("not a non-negative integer")
	}
	return int(i), nil
}

func schemaRat(v any) (*big.Rat, error) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, fmt.Errorf("not a number")
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return nil, fmt.Errorf("not a number")
	}
	return r, nil
}

// DisallowsUndeclared reports whether conversion must drop undeclared fields.
func (s *Schema) DisallowsUndeclared() bool {
	return s.additionalSet && !s.additionalAllow && s.additionalSchema == nil
}

// Properties returns the compiled property declarations.
func (s *Schema) Properties() map[string]*Schema { return s.props }

// Required returns the required property names.
func (s *Schema) Required() []string { return s.required }

var workStateSep = regexp.MustCompile(`[[:space:]-]+`)

// NormaliseWorkState matches the SQL state normalization used by node queries.
func NormaliseWorkState(state string) string {
	return workStateSep.ReplaceAllString(strings.ToLower(strings.TrimSpace(state)), "_")
}

// validateStateCatalog checks field_schema.states: objects with a distinct state
// and a category project counts understand.
func validateStateCatalog(val any) error {
	items, ok := val.([]any)
	if !ok {
		return fmt.Errorf("states must be an array")
	}
	seen := map[string]bool{}
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("states entries must be objects")
		}
		rawState, ok := obj["state"].(string)
		if !ok || strings.TrimSpace(rawState) == "" || len(rawState) > 64 {
			return fmt.Errorf("states entries need a state")
		}
		rawCategory, ok := obj["category"].(string)
		if !ok || strings.TrimSpace(rawCategory) == "" {
			return fmt.Errorf("states entries need a category")
		}
		norm := NormaliseWorkState(rawState)
		if norm == "" || seen[norm] {
			return fmt.Errorf("states entries must name a distinct state")
		}
		seen[norm] = true
		switch NormaliseWorkState(rawCategory) {
		case "open", "doing", "progress", "in_progress", "done", "cancelled", "canceled", "archived":
		default:
			return fmt.Errorf("states category must be open, doing, in_progress, done, cancelled or archived")
		}
	}
	return nil
}

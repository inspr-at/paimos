// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// This evaluator implements the exact keyword set of the pinned contracts.
// Numbers remain json.Number/big.Rat through type, bound and equality checks.
// A changed contract with a new keyword fails startup, rather than silently
// widening validation. All references resolve offline in the embedded bundle.
func (v *Validator) prepare(schema map[string]any) error {
	for key, value := range schema {
		switch key {
		case "$schema", "$id", "title", "description", "type", "required", "enum", "const", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "maxProperties", "format":
		case "$ref":
			parts := strings.SplitN(text(value), "#", 2)
			if parts[0] != "" && v.files[parts[0]] == nil {
				return fmt.Errorf("unbound schema reference")
			}
		case "pattern":
			p, err := regexp.Compile(text(value))
			if err != nil {
				return err
			}
			v.patterns[text(value)] = p
		case "properties", "$defs", "patternProperties":
			for name, child := range object(value) {
				if key == "patternProperties" {
					p, err := regexp.Compile(name)
					if err != nil {
						return err
					}
					v.patterns[name] = p
				}
				if err := v.prepare(object(child)); err != nil {
					return err
				}
			}
		case "items", "additionalProperties":
			if child := object(value); child != nil {
				if err := v.prepare(child); err != nil {
					return err
				}
			}
		case "oneOf", "allOf":
			for _, child := range array(value) {
				if err := v.prepare(object(child)); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported embedded schema keyword: %s", key)
		}
	}
	return nil
}
func (v *Validator) resolve(file, pointer string) map[string]any {
	value := any(v.files[file])
	if pointer != "" {
		for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			value = object(value)[part]
		}
	}
	return object(value)
}
func rational(value any) (*big.Rat, bool) {
	s, ok := value.(json.Number)
	if !ok {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(string(s))
	return r, ok
}
func equal(a, b any) bool {
	if x, ok := rational(a); ok {
		y, ok := rational(b)
		return ok && x.Cmp(y) == 0
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && a == y
	case bool:
		y, ok := b.(bool)
		return ok && a == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(a) != len(y) {
			return false
		}
		for i, x := range a {
			if !equal(x, y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(a) != len(y) {
			return false
		}
		for key, x := range a {
			z, exists := y[key]
			if !exists || !equal(x, z) {
				return false
			}
		}
		return true
	}
	return false
}
func matchesType(kind string, value any) bool {
	switch kind {
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		r, ok := rational(value)
		return ok && r.IsInt()
	case "number":
		_, ok := rational(value)
		return ok
	}
	return false
}
func (v *Validator) check(rule any, value any, file string, depth int) bool {
	if allowed, ok := rule.(bool); ok {
		return allowed
	}
	schema := object(rule)
	if schema == nil || depth > 128 {
		return false
	}
	if ref, ok := schema["$ref"]; ok {
		parts := strings.SplitN(text(ref), "#", 2)
		target := file
		if parts[0] != "" {
			target = parts[0]
		}
		pointer := ""
		if len(parts) == 2 {
			pointer = parts[1]
		}
		if !v.check(v.resolve(target, pointer), value, target, depth+1) {
			return false
		}
	}
	if kind, has := schema["type"]; has {
		allowed := []string{}
		if s, ok := kind.(string); ok {
			allowed = append(allowed, s)
		} else {
			for _, s := range array(kind) {
				allowed = append(allowed, text(s))
			}
		}
		matched := false
		for _, s := range allowed {
			if matchesType(s, value) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if expected, has := schema["const"]; has && !equal(value, expected) {
		return false
	}
	if choices, has := schema["enum"]; has {
		matched := false
		for _, choice := range array(choices) {
			if equal(value, choice) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if r, ok := rational(value); ok {
		if bound, has := schema["minimum"]; has {
			b, _ := rational(bound)
			if b == nil || r.Cmp(b) < 0 {
				return false
			}
		}
		if bound, has := schema["maximum"]; has {
			b, _ := rational(bound)
			if b == nil || r.Cmp(b) > 0 {
				return false
			}
		}
	}
	if s, ok := value.(string); ok {
		length := int64(utf8.RuneCountInString(s))
		if bound, has := schema["minLength"]; has && length < number(bound) {
			return false
		}
		if bound, has := schema["maxLength"]; has && length > number(bound) {
			return false
		}
		if p, has := schema["pattern"]; has && !v.patterns[text(p)].MatchString(s) {
			return false
		}
		if schema["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return false
			}
		}
	}
	if values, ok := value.([]any); ok {
		length := int64(len(values))
		if bound, has := schema["minItems"]; has && length < number(bound) {
			return false
		}
		if bound, has := schema["maxItems"]; has && length > number(bound) {
			return false
		}
		if schema["uniqueItems"] == true {
			for i, x := range values {
				for _, y := range values[:i] {
					if equal(x, y) {
						return false
					}
				}
			}
		}
		if child, has := schema["items"]; has {
			for _, x := range values {
				if !v.check(child, x, file, depth+1) {
					return false
				}
			}
		}
	}
	if obj, ok := value.(map[string]any); ok {
		if bound, has := schema["maxProperties"]; has && int64(len(obj)) > number(bound) {
			return false
		}
		for _, key := range array(schema["required"]) {
			if _, exists := obj[text(key)]; !exists {
				return false
			}
		}
		props := object(schema["properties"])
		for key, child := range obj {
			rule, matched := props[key]
			if matched && !v.check(rule, child, file, depth+1) {
				return false
			}
			for pattern, rule := range object(schema["patternProperties"]) {
				if v.patterns[pattern].MatchString(key) {
					matched = true
					if !v.check(rule, child, file, depth+1) {
						return false
					}
				}
			}
			if !matched {
				if schema["additionalProperties"] == false {
					return false
				}
				if rule := object(schema["additionalProperties"]); rule != nil && !v.check(rule, child, file, depth+1) {
					return false
				}
			}
		}
	}
	for _, sub := range array(schema["allOf"]) {
		if !v.check(sub, value, file, depth+1) {
			return false
		}
	}
	if choices, has := schema["oneOf"]; has {
		passing := 0
		for _, sub := range array(choices) {
			if v.check(sub, value, file, depth+1) {
				passing++
			}
		}
		if passing != 1 {
			return false
		}
	}
	return true
}

// Package schematest checks JSON against schema/stele.v1.json, the JSON
// Schema of stele.json (D88, ADR-0017), for tests. Only tests import it.
//
// Validate knows only the draft-07 keywords that schema uses: stele adds no
// dependency for a test (architecture §11). The website build checks the
// schema with a full validator as well.
package schematest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// ID is the schema's public address, its "$id".
const ID = "https://stele.lictorlabs.com/schema/stele.v1.json"

// File is the schema, relative to the module root.
const File = "schema/stele.v1.json"

// known are the keywords Validate checks.
var known = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "maxItems": true, "minLength": true, "maxLength": true,
	"enum": true, "const": true, "pattern": true, "not": true, "allOf": true,
	"if": true, "then": true, "else": true, "$ref": true, "definitions": true,
}

// annotations are keywords that never fail a value.
var annotations = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true,
	"default": true, "deprecationMessage": true,
}

// Root is the module root, found above the working directory.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = up
	}
}

// Load reads the schema from the module root.
func Load(t testing.TB) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Root(t), filepath.FromSlash(File)))
	if err != nil {
		t.Fatal(err)
	}
	var s any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("%s: %v", File, err)
	}
	return s
}

// Props is the sorted property names of the object schema at ptr, a
// "#/definitions/<name>" or "#" pointer, following an allOf of one $ref.
func Props(t testing.TB, schema any, ptr string) []string {
	t.Helper()
	s, err := resolve(schema, ptr)
	if err != nil {
		t.Fatal(err)
	}
	m := s.(map[string]any)
	if all, ok := m["allOf"].([]any); ok && len(all) == 1 && m["properties"] == nil {
		ref := all[0].(map[string]any)["$ref"].(string)
		return Props(t, schema, ref)
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no properties", ptr)
	}
	var out []string
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate reports why v, decoded by encoding/json into any, does not match
// schema, or nil.
func Validate(schema, v any) error {
	return check(schema, schema, v, "")
}

// resolve returns the schema at ptr: "#" or "#/definitions/<name>".
func resolve(root any, ptr string) (any, error) {
	if ptr == "#" {
		return root, nil
	}
	name, ok := strings.CutPrefix(ptr, "#/definitions/")
	if !ok {
		return nil, fmt.Errorf("$ref %q: only #/definitions/<name>", ptr)
	}
	defs, _ := root.(map[string]any)["definitions"].(map[string]any)
	s, ok := defs[name]
	if !ok {
		return nil, fmt.Errorf("$ref %q: no such definition", ptr)
	}
	return s, nil
}

// check matches v, at path at, against s; root resolves $ref.
func check(root, s, v any, at string) error {
	m, ok := s.(map[string]any)
	if !ok {
		return fmt.Errorf("schema at %q is not an object", at)
	}
	fail := func(format string, a ...any) error {
		where := at
		if where == "" {
			where = "(top)"
		}
		return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, a...))
	}
	for k := range m {
		if !known[k] && !annotations[k] {
			return fail("unknown keyword %q", k)
		}
	}
	if ref, ok := m["$ref"].(string); ok {
		sub, err := resolve(root, ref)
		if err != nil {
			return err
		}
		if err := check(root, sub, v, at); err != nil {
			return err
		}
	}
	if t, ok := m["type"]; ok {
		var types []string
		switch t := t.(type) {
		case string:
			types = []string{t}
		case []any:
			for _, x := range t {
				types = append(types, x.(string))
			}
		}
		match := false
		for _, name := range types {
			if isType(v, name) {
				match = true
			}
		}
		if !match {
			return fail("want %s", strings.Join(types, " or "))
		}
	}
	if e, ok := m["enum"].([]any); ok {
		in := false
		for _, x := range e {
			if reflect.DeepEqual(x, v) {
				in = true
			}
		}
		if !in {
			return fail("not one of %v", e)
		}
	}
	if c, ok := m["const"]; ok && !reflect.DeepEqual(c, v) {
		return fail("want %v", c)
	}
	if str, ok := v.(string); ok {
		n := float64(utf8.RuneCountInString(str))
		if x, ok := m["maxLength"].(float64); ok && n > x {
			return fail("longer than %v", x)
		}
		if x, ok := m["minLength"].(float64); ok && n < x {
			return fail("shorter than %v", x)
		}
		if p, ok := m["pattern"].(string); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				return fail("pattern: %v", err)
			}
			if !re.MatchString(str) {
				return fail("%q does not match %s", str, p)
			}
		}
	}
	if arr, ok := v.([]any); ok {
		if x, ok := m["maxItems"].(float64); ok && float64(len(arr)) > x {
			return fail("more than %v items", x)
		}
		if items, ok := m["items"]; ok {
			for i, x := range arr {
				if err := check(root, items, x, fmt.Sprintf("%s[%d]", at, i)); err != nil {
					return err
				}
			}
		}
	}
	if obj, ok := v.(map[string]any); ok {
		if req, ok := m["required"].([]any); ok {
			for _, k := range req {
				if _, has := obj[k.(string)]; !has {
					return fail("%q is required", k)
				}
			}
		}
		props, _ := m["properties"].(map[string]any)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub := at + "." + k
			if p, ok := props[k]; ok {
				if err := check(root, p, obj[k], sub); err != nil {
					return err
				}
				continue
			}
			switch ap := m["additionalProperties"].(type) {
			case bool:
				if !ap {
					return fail("%q is not allowed", k)
				}
			case map[string]any:
				if err := check(root, ap, obj[k], sub); err != nil {
					return err
				}
			}
		}
	}
	if all, ok := m["allOf"].([]any); ok {
		for _, sub := range all {
			if err := check(root, sub, v, at); err != nil {
				return err
			}
		}
	}
	if not, ok := m["not"]; ok {
		if check(root, not, v, at) == nil {
			return fail("matches what is not allowed: %v", not)
		}
	}
	if cond, ok := m["if"]; ok {
		next := m["else"]
		if check(root, cond, v, at) == nil {
			next = m["then"]
		}
		if next != nil {
			if err := check(root, next, v, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// isType reports whether v is of the JSON Schema type name.
func isType(v any, name string) bool {
	switch name {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return false
}

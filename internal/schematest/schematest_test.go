package schematest

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return v
}

func TestValidate(t *testing.T) {
	schema := decode(t, `{
		"type": "object",
		"additionalProperties": false,
		"required": ["a"],
		"properties": {
			"a": {"type": "string", "pattern": "^x", "maxLength": 3, "not": {"enum": ["xy"]}},
			"b": {"type": ["boolean", "null"]},
			"c": {"type": "array", "maxItems": 2, "items": {"$ref": "#/definitions/n"}},
			"d": {"if": {"const": 1}, "then": {"type": "number"}, "else": {"type": "string"}},
			"e": {"allOf": [{"type": "string"}, {"minLength": 2}]}
		},
		"definitions": {"n": {"type": "integer"}}
	}`)
	for _, ok := range []string{
		`{"a": "x"}`,
		`{"a": "xyz", "b": null}`,
		`{"a": "x", "b": true, "c": [1, 2]}`,
		`{"a": "x", "d": 1}`,
		`{"a": "x", "d": "s"}`,
		`{"a": "x", "e": "ab"}`,
	} {
		if err := Validate(schema, decode(t, ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`[]`,
		`{}`,
		`{"a": "y"}`,
		`{"a": "xyzw"}`,
		`{"a": "xy"}`,
		`{"a": "x", "z": 1}`,
		`{"a": "x", "b": 1}`,
		`{"a": "x", "c": [1, 2, 3]}`,
		`{"a": "x", "c": [1.5]}`,
		`{"a": "x", "d": true}`,
		`{"a": "x", "e": "a"}`,
	} {
		if err := Validate(schema, decode(t, bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestValidateRejectsUnknownKeyword(t *testing.T) {
	if err := Validate(decode(t, `{"minimum": 1}`), 2.0); err == nil {
		t.Error("an unknown keyword was ignored")
	}
}

// TestSchemaFile checks the published schema: draft-07, its public $id, only
// keywords Validate knows, and patterns Go compiles.
func TestSchemaFile(t *testing.T) {
	s := Load(t)
	m := s.(map[string]any)
	if m["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Errorf("$schema = %v", m["$schema"])
	}
	if m["$id"] != ID {
		t.Errorf("$id = %v, want %s", m["$id"], ID)
	}
	walk(t, "#", s)
}

// walk checks every keyword under the schema s at ptr.
func walk(t *testing.T, ptr string, s any) {
	m, ok := s.(map[string]any)
	if !ok {
		t.Errorf("%s: not a schema object", ptr)
		return
	}
	for k, v := range m {
		at := ptr + "/" + k
		switch k {
		case "properties", "definitions":
			for name, sub := range v.(map[string]any) {
				walk(t, at+"/"+name, sub)
			}
		case "items", "not", "if", "then", "else":
			walk(t, at, v)
		case "allOf":
			for _, sub := range v.([]any) {
				walk(t, at, sub)
			}
		case "additionalProperties":
			if _, ok := v.(bool); !ok {
				walk(t, at, v)
			}
		case "pattern":
			if _, err := regexp.Compile(v.(string)); err != nil {
				t.Errorf("%s: %v", at, err)
			}
		default:
			if !known[k] && !annotations[k] {
				t.Errorf("%s: keyword %q is not one Validate knows", at, k)
			}
		}
	}
}

func TestProps(t *testing.T) {
	got := Props(t, Load(t), "#/definitions/sink")
	if strings.Join(got, ",") != "config,name,share_briefs,timeout" {
		t.Errorf("Props = %v", got)
	}
}

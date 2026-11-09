package plugin

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/schematest"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// jsonTags is the json names of struct type v's fields.
func jsonTags(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestSchemaHasEveryKey: a key stele reads is in the schema, and the schema
// has no key stele does not read, so neither can gain one alone. The top
// level is what enforce.UnknownKeys knows, so stele's notes and the schema
// agree (D89). The schema's tables of accepted and refused files are in
// cmd/stele/schema_test.go.
func TestSchemaHasEveryKey(t *testing.T) {
	schema := schematest.Load(t)
	top := append([]string{enforce.KeySchema}, enforce.Keys()...)
	sort.Strings(top)
	for _, c := range []struct {
		ptr  string
		want []string
	}{
		{"#", top},
		{"#/definitions/plugin", jsonTags(entry{})},
		{"#/definitions/sink", jsonTags(sinkEntry{})},
		{"#/definitions/gitConfig", jsonTags(gitrepo.Config{})},
	} {
		got := schematest.Props(t, schema, c.ptr)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: schema has %v, stele reads %v", c.ptr, got, c.want)
		}
	}
}

// TestDollarSchemaIgnored: stele reads past "$schema".
func TestDollarSchemaIgnored(t *testing.T) {
	root := steleJSON(t, `{"$schema": "`+schematest.ID+`",
		"sources": [{"name": "git", "config": {"repo": "git@github.com:acme/g.git"}}, {"name": "docs"}],
		"checkers": [{"name": "lint"}], "sinks": [{"name": "warehouse"}]}`)
	items, err := Inventory(root, noGit{t}, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Err != nil {
			t.Errorf("%s %s: %v", it.Kind, it.Name, it.Err)
		}
	}
	if _, err := Sinks(root, &fakeRunner{}); err != nil {
		t.Fatal(err)
	}
	if _, err := enforce.Load(root); err != nil {
		t.Fatal(err)
	}
	if enforce.SetsBackstop(root) {
		t.Error(`"$schema" read as backstop`)
	}
}

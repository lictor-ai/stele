package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/gitexec"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/publish"
	"github.com/lictor-ai/stele/internal/schematest"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// foundPlugins finds every plugin and runs none.
type foundPlugins struct{}

func (foundPlugins) Find(kind plugin.Kind, name string) (string, error) {
	return filepath.Join(os.TempDir(), plugin.Exe(kind, name)), nil
}

func (foundPlugins) Run(context.Context, string, []byte, int, time.Duration) ([]byte, error) {
	return nil, errors.New("no plugin runs in this test")
}

// steleAccepts reports why stele refuses the stele.json content, or nil:
// as stele status reads it, with every plugin found and each repo checked
// as the git source checks it, and as stele publish reads "publish".
func steleAccepts(t *testing.T, content string) error {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := enforce.Load(root); err != nil {
		return err
	}
	items, err := plugin.Inventory(root, gitexec.NewFetcher(root), foundPlugins{})
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Err != nil {
			return it.Err
		}
	}
	if _, err := plugin.Sources(root, gitexec.NewFetcher(root), gitrepo.Pinned, foundPlugins{}); err != nil {
		return err
	}
	if _, err := plugin.Checkers(root, foundPlugins{}); err != nil {
		return err
	}
	if _, err := plugin.Sinks(root, foundPlugins{}); err != nil {
		return err
	}
	if strings.Contains(content, `"publish"`) {
		if _, err := publish.LoadConfig(root); err != nil {
			return err
		}
	}
	return nil
}

func schemaAccepts(t *testing.T, schema any, content string) error {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		t.Fatalf("%s: %v", content, err)
	}
	return schematest.Validate(schema, v)
}

// entries is a list of n distinct plugin entries.
func entries(n int) string {
	var names []string
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf(`{"name": "c%d"}`, i))
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// TestSchemaAcceptsWhatSteleAccepts: every stele.json stele takes, the
// schema takes too, so an editor never flags a working file.
func TestSchemaAcceptsWhatSteleAccepts(t *testing.T) {
	schema := schematest.Load(t)
	for _, c := range []string{
		`{}`,
		`{"$schema": "https://stele.lictorlabs.com/schema/stele.v1.json", "block_on_error": true}`,
		`{"block_on_error": false, "backstop": true}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:acme/guidelines.git"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "https://github.com/acme/guidelines", "ref": "v1.2", "path": "team/go.md"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "ssh://git@gitlab.example.com:2222/acme/g.git", "ref": "refs/tags/x"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "https://github.com/acme/g", "ref": "", "path": ""}}]}`,
		`{"sources": [{"name": "docs", "config": {"dir": "rules"}, "timeout": "1s"}, {"name": "wiki", "config": null, "timeout": "30m"}, {"name": "other"}]}`,
		`{"checkers": [{"name": "opengrep", "config": ["a", 1], "timeout": "1.5m"}]}`,
		`{"sinks": [{"name": "warehouse", "config": {"url": "x"}, "timeout": "90s", "share_briefs": false}]}`,
		`{"checkers": ` + entries(16) + `}`,
		`{"publish": {"repo": "https://github.com/acme/guidelines"}}`,
		// "" and null are what stele reads as unset.
		`{"sources": [{"name": "docs", "timeout": ""}], "checkers": [{"name": "lint", "timeout": ""}], "sinks": [{"name": "warehouse", "timeout": ""}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:acme/g.git"}, "timeout": ""}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:acme/g.git"}, "timeout": null}]}`,
		`{"block_on_error": null, "sources": null, "checkers": null, "sinks": null}`,
		`{"sources": [{"name": "docs", "timeout": null}], "checkers": [{"name": "lint", "timeout": null}], "sinks": [{"name": "warehouse", "timeout": null, "share_briefs": null}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:acme/g.git", "ref": null, "path": null}}]}`,
		`{"publish": {"repo": "git@github.com:acme/guidelines.git", "path": null}}`,
		// url.Parse reads a scheme in any case.
		`{"sources": [{"name": "git", "config": {"repo": "HTTPS://github.com/acme/g"}}]}`,
		`{"publish": {"repo": "Ssh://git@github.com/acme/g.git"}}`,
		`{"$schema": "x", "block_on_error": false, "backstop": false,
		  "sources": [{"name": "git", "config": {"repo": "git@github.com:acme/g.git", "ref": "main", "path": "stele.md"}}, {"name": "docs"}],
		  "checkers": [{"name": "lint"}], "sinks": [{"name": "warehouse", "share_briefs": true}],
		  "publish": {"repo": "git@github.com:acme/guidelines.git", "path": "project-a/stele.md"}}`,
	} {
		if err := steleAccepts(t, c); err != nil {
			t.Errorf("stele refuses %s: %v", c, err)
			continue
		}
		if err := schemaAccepts(t, schema, c); err != nil {
			t.Errorf("the schema refuses %s: %v", c, err)
		}
	}
}

// TestSchemaRejectsWhatSteleRejects: a mistake stele refuses, an editor
// flags as it is typed.
func TestSchemaRejectsWhatSteleRejects(t *testing.T) {
	schema := schematest.Load(t)
	for _, c := range []string{
		`{"block_on_error": "yes"}`,
		`{"sources": [{"name": "docs", "conf": {}}]}`,
		`{"sinks": [{"name": "warehouse", "share_brief": true}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "branch": "main"}}]}`,
		`{"sources": [{"name": "git"}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git"}, "timeout": "1m"}]}`,
		`{"sources": [{"name": "git", "config": {}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "file:///x"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "/srv/g.git"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "https://me@github.com/a/b"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "path": "../x.md"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "path": "a.txt"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "ref": "refs/remotes/x"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "ref": "a..b"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "ref": "x.lock"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/b.git", "ref": "` + strings.Repeat("a", 201) + `"}}]}`,
		`{"sources": [{"name": "Bad"}]}`,
		`{"sources": [{"name": "x/y"}]}`,
		`{"sources": [{"name": "stelemd"}]}`,
		`{"checkers": [{"name": "regex"}]}`,
		`{"checkers": [{"name": "git"}]}`,
		`{"checkers": [{"name": "lint", "timeout": "soon"}]}`,
		`{"checkers": [{}]}`,
		`{"checkers": ` + entries(17) + `}`,
		`{"checkers": {"name": "lint"}}`,
		`{"publish": {}}`,
		`{"publish": {"repo": "file:///x"}}`,
		`{"publish": {"repo": "git@github.com:a/b.git", "path": "../x.md"}}`,
		`{"publish": {"repo": "git@github.com:a/b.git", "path": "a./x.md"}}`,
		`{"publish": {"repo": "git@github.com:a/b.git", "brnach": "x"}}`, // D89
	} {
		if err := steleAccepts(t, c); err == nil {
			t.Errorf("stele accepts %s", c)
		}
		if err := schemaAccepts(t, schema, c); err == nil {
			t.Errorf("the schema accepts %s", c)
		}
	}
	// Only stele refuses these: a schema cannot see a duration's value or
	// every rule of a URL. Nor a name listed twice, which stele compile and
	// stele check refuse.
	for _, c := range []string{
		`{"checkers": [{"name": "lint", "timeout": "1h"}]}`,
		`{"checkers": [{"name": "lint"}, {"name": "lint"}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "ssh://me:secret@github.com/a/b"}}]}`,
	} {
		if err := steleAccepts(t, c); err == nil {
			t.Errorf("stele accepts %s", c)
		}
		if err := schemaAccepts(t, schema, c); err != nil {
			t.Errorf("the schema refuses %s, which this test says only stele does: %v", c, err)
		}
	}
	// Only the schema refuses these, on purpose (ADR-0017): stele notes a
	// top-level key it does not know and goes on (D89), encoding/json
	// matches a key in another case, and net/url takes some odd forms of a
	// URL; an editor should flag each.
	for _, c := range []string{
		`{"block_on_eror": true}`,
		`{"checkers": [{"Name": "lint"}]}`,
		`{"sources": [{"name": "git", "config": {"Repo": "git@github.com:a/b.git"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "https://github.com:/a/b"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "ssh://@github.com/a/b"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "ssh://a@b@github.com/a/b"}}]}`,
		`{"sources": [{"name": "git", "config": {"repo": "https://github.com/a/b#"}}]}`,
	} {
		if err := schemaAccepts(t, schema, c); err == nil {
			t.Errorf("the schema accepts %s", c)
		}
		if err := steleAccepts(t, c); err != nil {
			t.Errorf("stele refuses %s, which this test says only the schema does: %v", c, err)
		}
	}
}

// TestSchemaTopLevelIsWhatSteleKnows: a top-level key the schema refuses is
// one stele notes, and one it takes stele does not note (D89).
func TestSchemaTopLevelIsWhatSteleKnows(t *testing.T) {
	schema := schematest.Load(t)
	for _, c := range []struct {
		content string
		noted   bool
	}{
		{`{"$schema": "x", "block_on_error": true, "backstop": true, "sources": [], "checkers": [], "sinks": []}`, false},
		{`{"block_on_eror": true}`, true},
		{`{"$Schema": "x"}`, true},
		{`{"plugins": []}`, true},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if noted := len(enforce.UnknownKeys(root)) > 0; noted != c.noted {
			t.Errorf("%s: stele notes %v, want %v", c.content, enforce.UnknownKeys(root), c.noted)
		}
		if refused := schemaAccepts(t, schema, c.content) != nil; refused != c.noted {
			t.Errorf("%s: the schema refuses it: %v, want %v", c.content, refused, c.noted)
		}
	}
}

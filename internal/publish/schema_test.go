package publish

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/schematest"
)

// TestSchemaHasPublishKeys: the schema's "publish" has the keys stele reads,
// and no other.
func TestSchemaHasPublishKeys(t *testing.T) {
	var want []string
	rt := reflect.TypeOf(fileConfig{})
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		want = append(want, name)
	}
	sort.Strings(want)
	got := schematest.Props(t, schematest.Load(t), "#/definitions/publish")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schema has %v, stele reads %v", got, want)
	}
}

// TestLoadConfigRefusesUnknownKey: a typo under "publish" is an error, never
// a default in its place: "pth" must not publish to stele.md (D89).
func TestLoadConfigRefusesUnknownKey(t *testing.T) {
	for _, c := range []struct{ content, key string }{
		{`{"publish": {"repo": "git@github.com:me/g.git", "pth": "team/a.md"}}`, `"pth"`},
		{`{"publish": {"repo": "git@github.com:me/g.git", "branch": "x"}}`, `"branch"`},
	} {
		_, err := LoadConfig(writeJSON(t, c.content))
		if !errors.Is(err, ErrBadConfig) {
			t.Errorf("%s: err = %v, want ErrBadConfig", c.content, err)
			continue
		}
		for _, want := range []string{c.key, `"repo"`, `"path"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %q, want it to show %s", c.content, err, want)
			}
		}
	}
	// encoding/json matches a key in another case, as for every other key.
	cfg, err := LoadConfig(writeJSON(t, `{"publish": {"Repo": "git@github.com:me/g.git", "PATH": "a.md"}}`))
	if err != nil || cfg.Path != "a.md" {
		t.Errorf("LoadConfig = %+v, %v", cfg, err)
	}
	// Other top-level keys are not publish's to refuse.
	if _, err := LoadConfig(writeJSON(t, `{"block_on_eror": true, "publish": {"repo": "git@github.com:me/g.git"}}`)); err != nil {
		t.Errorf("LoadConfig: %v", err)
	}
}

// TestLoadConfigIgnoresDollarSchema: stele reads past "$schema".
func TestLoadConfigIgnoresDollarSchema(t *testing.T) {
	root := t.TempDir()
	content := `{"$schema": "` + schematest.ID + `", "publish": {"repo": "git@github.com:acme/guidelines.git"}}`
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "git@github.com:acme/guidelines.git" || cfg.Path != DefaultPath {
		t.Errorf("LoadConfig = %+v", cfg)
	}
}

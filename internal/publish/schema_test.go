package publish

import (
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

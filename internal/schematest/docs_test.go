package schematest

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/enforce"
)

// fence is one ```json block of a Markdown file.
type fence struct {
	file string
	line int // of the ```json line, from 1
	text string
}

// jsonFences returns the ```json blocks of the Markdown text.
func jsonFences(file, text string) []fence {
	var out []fence
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```json" {
			continue
		}
		start := i + 1
		var body []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
			body = append(body, lines[i])
		}
		out = append(out, fence{file: file, line: start, text: strings.Join(body, "\n")})
	}
	return out
}

// steleJSON reports whether the fence is a stele.json: an object with a key
// stele.json has, and none of the log, the reports or the plugin protocol.
// A template, with … or <name> in it, is not.
func steleJSON(f fence) (map[string]any, bool) {
	if strings.Contains(f.text, "…") || strings.Contains(f.text, "<") {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(f.text), &m) != nil {
		return nil, false
	}
	for _, k := range []string{"v", "protocol", "op"} {
		if _, ok := m[k]; ok {
			return nil, false
		}
	}
	for _, k := range enforce.Keys() {
		if _, ok := m[k]; ok {
			return m, true
		}
	}
	return nil, false
}

// TestDocsExamples: every stele.json the README and docs show is one the
// schema accepts.
func TestDocsExamples(t *testing.T) {
	root := Root(t)
	files := []string{filepath.Join(root, "README.md")}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := Load(t)
	n := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, file)
		for _, f := range jsonFences(filepath.ToSlash(rel), string(data)) {
			v, ok := steleJSON(f)
			if !ok {
				continue
			}
			n++
			if err := Validate(schema, any(v)); err != nil {
				t.Errorf("%s:%d: %v", f.file, f.line, err)
			}
		}
	}
	// The README and the docs hold more than a dozen: fewer means the
	// fences were not found.
	if n < 12 {
		t.Errorf("found %d stele.json examples, want at least 12", n)
	}
}

func TestJSONFences(t *testing.T) {
	text := "a\r\n```json\r\n{\"sinks\": []}\r\n```\r\n```\r\nx\r\n```\n```json\n{\"v\": 1, \"sinks\": []}\n```\n"
	fs := jsonFences("x.md", text)
	if len(fs) != 2 || fs[0].line != 2 || fs[0].text != `{"sinks": []}` {
		t.Fatalf("fences = %+v", fs)
	}
	if _, ok := steleJSON(fs[0]); !ok {
		t.Error("a stele.json was not one")
	}
	if _, ok := steleJSON(fs[1]); ok {
		t.Error("a versioned format was taken for stele.json")
	}
}

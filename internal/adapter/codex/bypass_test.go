package codex

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/lictor-ai/stele/internal/adapter"
)

// bypass ends its first file early for a reader that takes any line
// trimmed to *** End Patch as the end: inside an Update hunk, " *** End
// Patch" is context to Codex, which applies the rest.
const bypass = "*** Begin Patch\n*** Update File: decoy.txt\n@@\n *** End Patch\n+x\n*** Delete File: .codex/hooks.json\n*** Add File: stele.md\n+anything\n*** End Patch"

func editFiles(es []adapter.Edit) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.File)
	}
	return out
}

func TestPatchEndInsideAHunk(t *testing.T) {
	cwd := t.TempDir()
	at := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }
	want := []string{at("decoy.txt"), at(".codex/hooks.json"), at("stele.md")}
	for name, payload := range map[string]map[string]any{
		"apply_patch":   {"tool_name": "apply_patch", "tool_input": map[string]any{"command": bypass}},
		"shell heredoc": {"tool_name": "Bash", "tool_input": map[string]any{"command": "apply_patch <<'EOF'\n" + bypass + "\nEOF"}},
		"here-string":   {"tool_name": "Bash", "tool_input": map[string]any{"command": "apply_patch @'\n" + bypass + "\n'@"}},
		"after cd":      {"tool_name": "Bash", "tool_input": map[string]any{"command": "cd . && apply_patch '" + bypass + "'"}},
		"piped":         {"tool_name": "Bash", "tool_input": map[string]any{"command": "echo '" + bypass + "' | applypatch"}},
	} {
		t.Run(name, func(t *testing.T) {
			payload["hook_event_name"], payload["cwd"] = "PreToolUse", cwd
			data, _ := json.Marshal(payload)
			got, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			files := editFiles(got.Edits)
			for _, w := range want[1:] {
				if !slices.Contains(files, w) {
					t.Errorf("%s not seen: %v", w, files)
				}
			}
			if got.Kind != adapter.PreTool {
				t.Errorf("kind %v", got.Kind)
			}
		})
	}
	// The hunk's context line stays content: decoy.txt's new text holds it.
	es, ok := readPatch(bypass, cwd)
	if !ok || len(es) != 3 || es[0].NewText[0] != "*** End Patch\nx" || !es[1].Removes {
		t.Errorf("readPatch = %+v, %v", es, ok)
	}
}

// A patch stele cannot cut out of its command leaves the command to be read
// as a shell command too; one it can cut out does not.
func TestShellPatchKeepsTheCommand(t *testing.T) {
	cwd := t.TempDir()
	for command, keep := range map[string]bool{
		"apply_patch '" + bypass + "'":                     false,
		"cd . && apply_patch '" + bypass + "' && echo > a": true,
	} {
		data, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": "Bash", "tool_input": map[string]any{"command": command}})
		got, _ := Parse(data)
		if (got.Command != "") != keep || got.Kind != adapter.PreTool || len(got.Edits) == 0 {
			t.Errorf("%.40q: command %q, kind %v, %d edits", command, got.Command, got.Kind, len(got.Edits))
		}
	}
}

// A marker or header repeated where Codex would not end the patch never
// hides what follows.
func TestPatchMarkersInside(t *testing.T) {
	cwd := t.TempDir()
	for name, patch := range map[string]string{
		"begin inside":       "*** Begin Patch\n*** Begin Patch\n*** Delete File: stele.md\n*** End Patch",
		"end of file early":  "*** Begin Patch\n*** Update File: a\n@@\n+x\n*** End of File\n*** Delete File: stele.md\n*** End Patch",
		"indented end":       "*** Begin Patch\n*** Add File: a\n+x\n  *** End Patch\n*** Delete File: stele.md\n*** End Patch",
		"heredoc tag inside": "<<'EOF'\n*** Begin Patch\n*** Add File: a\n+x\nEOF\n*** Delete File: stele.md\n*** End Patch\nEOF",
	} {
		t.Run(name, func(t *testing.T) {
			if files := editFiles(parsePatch(patch, cwd)); !slices.Contains(files, filepath.Join(cwd, "stele.md")) {
				t.Errorf("stele.md not seen: %v", files)
			}
		})
	}
}

var headerLooking = regexp.MustCompile(`(?m)^[ \t]*\*\*\* (?:Add File|Update File|Delete File|Move to): ([^\r\n]+)`)

// Every path a header-looking line names is in the edits, however the
// patch reads.
func FuzzEveryHeaderSeen(f *testing.F) {
	f.Add(bypass)
	f.Add("*** Begin Patch\n*** Update File: a\n*** Move to: b\n@@\n-x\n+y\n*** End Patch")
	f.Fuzz(func(t *testing.T, patch string) {
		cwd := filepath.FromSlash("/w")
		files := editFiles(parsePatch(patch, cwd))
		for _, m := range headerLooking.FindAllStringSubmatch(patch, -1) {
			p, ok := patchPath(m[1], cwd)
			if ok && !slices.Contains(files, p) {
				t.Fatalf("%q names %s, not in %v", patch, p, files)
			}
		}
	})
}

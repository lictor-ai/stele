package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// fresh is what Install makes of no settings at all.
const fresh = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "stele hook --installed=v1.2.0",
            "timeout": 10
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "stele hook --installed=v1.2.0",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
`

// user is a settings file with the user's own content, including a hook on
// the same tools as stele's.
const user = `{
  "permissions": {
    "allow": [
      "Bash(go test:*)"
    ]
  },
  "env": {
    "N": 1e3,
    "BIG": 12345678901234567890,
    "NAME": "caf\u00e9 <&>"
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "./guard.sh"
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Write|Edit",
        "hooks": [
          {
            "type": "command",
            "command": "gofmt -w"
          }
        ]
      }
    ]
  },
  "model": "opus"
}
`

func TestInstallFresh(t *testing.T) {
	for _, in := range []string{"", "  \n", "{}"} {
		out, changed, notes, err := Install([]byte(in), "v1.2.0")
		if err != nil || !changed || len(notes) != 0 {
			t.Fatalf("Install(%q) = changed %v, notes %q, err %v", in, changed, notes, err)
		}
		if diff := cmp.Diff(fresh, string(out)); diff != "" {
			t.Errorf("Install(%q) mismatch (-want +got):\n%s", in, diff)
		}
	}
}

func TestInstallKeepsUserSettings(t *testing.T) {
	out, changed, _, err := Install([]byte(user), "v1.2.0")
	if err != nil || !changed {
		t.Fatalf("Install = changed %v, err %v", changed, err)
	}
	s := string(out)
	for _, want := range []string{`"N": 1e3`, `"BIG": 12345678901234567890`, `"caf\u00e9 <&>"`, `"./guard.sh"`, `"gofmt -w"`, `"model": "opus"`} {
		if !strings.Contains(s, want) {
			t.Errorf("output lost %s:\n%s", want, s)
		}
	}
	// Key order kept, stele's group after the user's on each event.
	order := []string{`"permissions"`, `"env"`, `"hooks"`, `"PreToolUse"`, `"./guard.sh"`, `stele hook`, `"PostToolUse"`, `"gofmt -w"`, `stele hook`, `"model"`}
	at := 0
	for _, w := range order {
		i := strings.Index(s[at:], w)
		if i < 0 {
			t.Fatalf("%s missing or out of order after offset %d:\n%s", w, at, s)
		}
		at += i + len(w)
	}

	// Uninstall gives back the file it started from.
	back, removed, empty, err := Uninstall(out)
	if err != nil || removed != 2 || empty {
		t.Fatalf("Uninstall = removed %d, empty %v, err %v", removed, empty, err)
	}
	if diff := cmp.Diff(user, string(back)); diff != "" {
		t.Errorf("Uninstall(Install(user)) mismatch (-want +got):\n%s", diff)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	odd := strings.ReplaceAll(fresh, "  ", "\t") // not how stele would write it
	for _, in := range []string{fresh, odd} {
		out, changed, _, err := Install([]byte(in), "v1.2.0")
		if err != nil || changed {
			t.Fatalf("Install again = changed %v, err %v", changed, err)
		}
		if string(out) != in {
			t.Errorf("Install again rewrote the file:\n%s", out)
		}
	}
}

func TestInstallReplacesOldStamps(t *testing.T) {
	old := strings.ReplaceAll(fresh, "v1.2.0", "v1.1.0")
	out, changed, _, err := Install([]byte(old), "v1.2.0")
	if err != nil || !changed {
		t.Fatalf("Install = changed %v, err %v", changed, err)
	}
	if diff := cmp.Diff(fresh, string(out)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}

	// Two stele groups on an event collapse to one.
	twice, _, _, _ := Install([]byte(old), "v1.1.0")
	var s map[string]map[string][]json.RawMessage
	json.Unmarshal(twice, &s)
	s["hooks"]["PreToolUse"] = append(s["hooks"]["PreToolUse"], s["hooks"]["PreToolUse"][0])
	dup, _ := json.Marshal(s)
	out, changed, _, err = Install(dup, "v1.2.0")
	if err != nil || !changed || strings.Count(string(out), "stele hook") != 2 {
		t.Errorf("Install over duplicates = changed %v, err %v:\n%s", changed, err, out)
	}

	// One event only is repaired.
	var one map[string]map[string]json.RawMessage
	json.Unmarshal([]byte(fresh), &one)
	delete(one["hooks"], "PostToolUse")
	half, _ := json.Marshal(one)
	out, changed, _, err = Install(half, "v1.2.0")
	if err != nil || !changed || strings.Count(string(out), "stele hook --installed=v1.2.0") != 2 {
		t.Errorf("Install over one event = changed %v, err %v:\n%s", changed, err, out)
	}
}

func TestInstallLeavesHandWrittenHooks(t *testing.T) {
	in := `{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"stele hook"}]}]}}`
	out, changed, notes, err := Install([]byte(in), "v1.2.0")
	if err != nil || !changed {
		t.Fatalf("Install = changed %v, err %v", changed, err)
	}
	if !strings.Contains(string(out), `"command": "stele hook"`) {
		t.Errorf("hand-written hook removed:\n%s", out)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "PreToolUse") {
		t.Errorf("notes = %q, want one naming PreToolUse", notes)
	}
	// Uninstall leaves it too.
	back, removed, _, err := Uninstall(out)
	if err != nil || removed != 2 || !strings.Contains(string(back), `"stele hook"`) {
		t.Errorf("Uninstall = removed %d, err %v:\n%s", removed, err, back)
	}
}

func TestUninstall(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantRemoved int
		wantEmpty   bool
		want        string // "" when the output is not compared
	}{
		{name: "fresh", in: fresh, wantRemoved: 2, wantEmpty: true, want: "{}\n"},
		{
			name: "shared group keeps the user's handler",
			in: `{"hooks":{"PreToolUse":[{"matcher":"Write|Edit|MultiEdit","hooks":[` +
				`{"type":"command","command":"stele hook --installed=v1"},{"type":"command","command":"./mine.sh"}]}]}}`,
			wantRemoved: 1,
			want: `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "./mine.sh"
          }
        ]
      }
    ]
  }
}
`,
		},
		{
			name: "edited matcher and timeout",
			in: `{"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[` +
				`{"type":"command","command":"/usr/local/bin/stele hook --installed=v0.9.0","timeout":60}]}]},"model":"x"}`,
			wantRemoved: 1,
			want:        "{\n  \"model\": \"x\"\n}\n",
		},
		{
			name:        "windows path to stele.exe",
			in:          `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"C:\\tools\\stele.exe hook --installed=v1"}]}]}}`,
			wantRemoved: 1,
			wantEmpty:   true,
			want:        "{}\n",
		},
		{name: "nothing to remove", in: user, wantRemoved: 0, want: user},
		{name: "empty file", in: "", wantRemoved: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, removed, empty, err := Uninstall([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if removed != tt.wantRemoved || empty != tt.wantEmpty {
				t.Errorf("Uninstall = removed %d, empty %v, want %d, %v", removed, empty, tt.wantRemoved, tt.wantEmpty)
			}
			if diff := cmp.Diff(tt.want, string(out)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSettingsRefusesMalformed(t *testing.T) {
	tests := []struct{ name, in, wantErr string }{
		{"invalid", "{\n  \"a\": 1,\n  \"b\": }", "line 3"},
		{"comment", "{\n  // mine\n  \"a\": 1\n}", "line 2"},
		{"trailing comma", `{"a": 1,}`, "invalid JSON"},
		{"array", `[]`, "not a JSON object"},
		{"null", `null`, "not a JSON object"},
		{"hooks not an object", `{"hooks": "x"}`, `"hooks" is not`},
		{"event not an array", `{"hooks": {"PreToolUse": {}}}`, `"PreToolUse" is not`},
		{"group hooks not an array", `{"hooks": {"PreToolUse": [{"hooks": 1}]}}`, `"hooks" in a PreToolUse group`},
		{"duplicate key", `{"hooks": {}, "hooks": {}}`, `"hooks" appears twice`},
		{"duplicate event", `{"hooks": {"PostToolUse": [], "PostToolUse": []}}`, `"PostToolUse" appears twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, _, err := Install([]byte(tt.in), "v1")
			if err == nil || out != nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Install = %q, %v, want an error containing %q", out, err, tt.wantErr)
			}
			out, _, _, err = Uninstall([]byte(tt.in))
			if err == nil || out != nil {
				t.Errorf("Uninstall = %q, %v, want an error", out, err)
			}
		})
	}
}

func TestSettingsKeepsLineEndsAndBOM(t *testing.T) {
	in := "\ufeff" + strings.ReplaceAll(user, "\n", "\r\n")
	out, _, _, err := Install([]byte(in), "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "\ufeff{\r\n") || strings.Count(string(out), "\n") != strings.Count(string(out), "\r\n") {
		t.Errorf("line ends or BOM lost:\n%q", out)
	}
	back, _, _, err := Uninstall(out)
	if err != nil || string(back) != in {
		t.Errorf("Uninstall(Install) = %q, %v, want the input", back, err)
	}
}

func TestInstalled(t *testing.T) {
	if v, ok := Installed([]byte(fresh)); !ok || v != "v1.2.0" {
		t.Errorf("Installed(fresh) = %q, %v", v, ok)
	}
	for _, in := range []string{"", user, "not json", `{"hooks":{"PreToolUse":[{"hooks":[{"command":"stele hook"}]}]}}`} {
		if v, ok := Installed([]byte(in)); ok {
			t.Errorf("Installed(%q) = %q, want none", in, v)
		}
	}
}

func TestHasSteleHook(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{fresh, true},
		{`{"hooks":{"PostToolUse":[{"hooks":[{"command":"stele hook"}]}]}}`, true},
		{`{"hooks":{"PreToolUse":[{"hooks":[{"command":"stele","args":["hook"]}]}]}}`, true},
		{user, false},
		{"", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		if got := HasSteleHook([]byte(tt.in)); got != tt.want {
			t.Errorf("HasSteleHook(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestInstalledVersion(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--installed=v1.2.0"}, "v1.2.0"},
		{[]string{"--installed="}, ""},
		{[]string{"x", "--installed=devel", "y"}, "devel"},
		{[]string{"-installed=v1"}, ""},
	}
	for _, tt := range tests {
		if got := InstalledVersion(tt.args); got != tt.want {
			t.Errorf("InstalledVersion(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeHome points the home directory at a temp directory.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// jsonEscaped is s as it appears inside a JSON string.
func jsonEscaped(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Trim(string(b), `"`)
}

// fakeEnv is a scrubEnv of made-up values, under temp directories.
func fakeEnv(t *testing.T) scrubEnv {
	return scrubEnv{
		work:    t.TempDir(),
		tmp:     os.TempDir(),
		home:    t.TempDir(),
		user:    "fakeuser",
		account: []string{"00000000-0000-4000-8000-0000000000c1", "owner@account.test"},
	}
}

func TestScrubTranscript(t *testing.T) {
	env := fakeEnv(t)
	home := env.home
	fwd := filepath.ToSlash(home)
	msys := fwd
	if m := drivePath.FindStringSubmatch(home); m != nil {
		msys = "/" + strings.ToLower(m[1]) + "/" + filepath.ToSlash(m[2])
	}
	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(home, "-")
	raw, err := os.ReadFile(filepath.Join("testdata", "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReplacer(
		"{{HOME}}", jsonEscaped(t, home), "{{WORK}}", jsonEscaped(t, env.work),
		"{{FWD}}", fwd, "{{MSYS}}", msys, "{{SLUG}}", slug, "{{USER}}", "FakeUser",
	).Replace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	// Hook output nested in a tool result: a path escaped twice.
	nested, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{
		"content": `{"cwd":"` + jsonEscaped(t, filepath.Join(home, "proj")) + `"}`}})
	in += string(nested) + "\n"

	s := newScrubberFor(env)
	out := string(s.jsonl([]byte(in)))

	if what := s.leak([]byte(out)); what != "" {
		t.Errorf("scrubbed transcript still holds %s:\n%s", what, out)
	}
	for _, gone := range []string{
		"fake.person@corp.internal", "Co-Authored-By", "Fake Person", "Fake Org",
		"00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b",
		"00000000-0000-4000-8000-0000000000c1", "OWNER@ACCOUNT.TEST",
		"memory_paths", "userID", "user_id", "fake-user-id", "oauthAccount", "userEmail", `"path"`,
		"FAKE-SYSTEM-PROMPT", "FakeUser 197121", "owner FakeUser,", "~ice",
	} {
		if strings.Contains(out, gone) {
			t.Errorf("scrubbed transcript still holds %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{
		`"cwd":"<run>"`, `"cwd":"~"`, "mail <email>, eval@example.com, dev@example.org, a@b.test or git@github.com:o/r",
		"see ~/a, ~/b, ~-c and <tmp>/", "ice/d", "12345678901234567890", `"name":"fake-plugin"`,
		`"type":"credential_org"`, "not JSON: ~", "1 <user> 197121", "owner <user>, not FakeUserx",
		"account <account> and <email>", `{"type":"api-request-blob","removed":"by the eval: the raw API request"}`,
		`{\"cwd\":\"~\\\\proj\"}`,
	} {
		if strings.Contains(kept, `~\\\\proj`) && filepath.Separator == '/' {
			kept = `{\"cwd\":\"~/proj\"}`
		}
		if !strings.Contains(out, kept) {
			t.Errorf("scrubbed transcript lacks %q:\n%s", kept, out)
		}
	}
	if !s.orgs["00000000-0000-4000-8000-00000000000a"] {
		t.Errorf("organizationUuid not recorded: %v", s.orgs)
	}
	if what := s.leak([]byte(in)); what == "" {
		t.Error("leak found nothing in the raw transcript")
	}
}

func TestPathsKeepTheAgentsEmails(t *testing.T) {
	s := newScrubberFor(fakeEnv(t))
	in := "+// Contact: maintainer@corp.internal, owner@account.test\n"
	want := "+// Contact: maintainer@corp.internal, <email>\n"
	if got := string(s.paths([]byte(in))); got != want {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

func TestPathBoundaries(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "al")
	s := newScrubberFor(scrubEnv{home: home})
	sep := string(filepath.Separator)
	for in, want := range map[string]string{
		home + sep + "x":                  "~" + sep + "x",
		"(" + home + ")":                  "(~)",
		home + "ice" + sep + "x":          home + "ice" + sep + "x",
		filepath.Join(base, "var", "al"):  filepath.Join(base, "var", "al"),
		"cd " + home + ". Then":           "cd ~. Then",
		"two: " + home + ";" + home + sep: "two: ~;~" + sep,
	} {
		if got := string(s.paths([]byte(in))); got != want {
			t.Errorf("paths(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckLeaks(t *testing.T) {
	env := fakeEnv(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "go-logging-stele-1")
	if err := os.MkdirAll(base+".setup", 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(base+".diff", []byte("clean"), 0o644)
	os.WriteFile(filepath.Join(dir, "go-logging-stele-10.diff"), []byte(env.home), 0o644) // another run
	s := newScrubberFor(env)
	if err := s.checkLeaks(base); err != nil {
		t.Errorf("checkLeaks on clean files: %v", err)
	}
	leaky := filepath.Join(base+".setup", "CLAUDE.md")
	os.WriteFile(leaky, []byte("ask the FAKEUSER account"), 0o644)
	err := s.checkLeaks(base)
	if err == nil || !strings.Contains(err.Error(), "CLAUDE.md (the account name)") {
		t.Errorf("checkLeaks = %v, want CLAUDE.md named", err)
	}
	if b, _ := os.ReadFile(leaky); strings.Contains(string(b), "FAKEUSER") || !strings.HasPrefix(string(b), "removed by the eval") {
		t.Errorf("leaky file not replaced: %q", b)
	}
}

func TestHomeless(t *testing.T) {
	home := fakeHome(t)
	sep := string(filepath.Separator)
	for in, want := range map[string]string{
		home + sep + "bin":     "~" + sep + "bin",
		"at " + home:           "at ~",
		"(" + home + ")":       "(~)",
		home + "x" + sep + "y": home + "x" + sep + "y",
	} {
		if got := homeless(in); got != want {
			t.Errorf("homeless(%q) = %q, want %q", in, got, want)
		}
	}
	root := filepath.VolumeName(home) + sep
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	if in := root + "bin"; homeless(in) != in {
		t.Errorf("homeless(%q) = %q with home at the root, want it unchanged", in, homeless(in))
	}
}

func TestCommonAccountNameKept(t *testing.T) {
	s := newScrubberFor(scrubEnv{user: "Runner"})
	in := "the runner ran as root"
	if got := string(s.paths([]byte(in))); got != in {
		t.Errorf("paths = %q, want %q unchanged", got, in)
	}
}

func TestWithoutLeaks(t *testing.T) {
	s := newScrubberFor(fakeEnv(t))
	r := Result{Scenario: "a", Arm: "stele", Rep: 1, Error: "boom",
		Grade: Grade{Violations: []string{"x.go:1: fine", "y.go:2: owned by fakeuser"}, Notes: []string{"note"}}}
	got := withoutLeaks(s, r, "the account name")
	if want := []string{"x.go:1: fine", "removed by the eval: private data"}; !slices.Equal(got.Violations, want) {
		t.Errorf("violations = %q, want %q", got.Violations, want)
	}
	if !strings.HasPrefix(got.Error, "boom; its result still held the account name") {
		t.Errorf("error = %q", got.Error)
	}
}

func TestScrubPATH(t *testing.T) {
	env := fakeEnv(t)
	base := t.TempDir()
	homeBin := filepath.Join(env.home, "bin")
	tool := filepath.Join(env.home, "Local", "FakeTool", "bin")
	vendor := filepath.Join(base, "FakeVendor", "bin")
	spaced := filepath.Join(base, "Other Vendor", "bin")
	third := filepath.Join(base, "ToolC", "bin")
	// System dirs a Linux or macOS PATH holds are not the user's own.
	raw := strings.Join([]string{homeBin, "/usr/bin", tool, vendor, spaced, "/bin", third,
		"/usr/local/go/bin", "/snap/bin", "/usr/games", "/opt/homebrew/bin"}, string(os.PathListSeparator))
	t.Setenv("PATH", raw)
	env.path = filepath.SplitList(os.Getenv("PATH"))
	s := newScrubberFor(env)

	// As Git Bash prints it: MSYS drives, the home as ~, and its own dirs.
	msys := func(p string) string {
		if m := drivePath.FindStringSubmatch(p); m != nil {
			return "/" + strings.ToLower(m[1]) + "/" + filepath.ToSlash(m[2])
		}
		return p
	}
	tilde := func(p string) string { return "~" + filepath.ToSlash(strings.TrimPrefix(p, env.home)) }
	bash := strings.Join([]string{tilde(homeBin), "/mingw64/bin", "/usr/bin", tilde(tool), msys(vendor), msys(spaced), "/tmp/stele-eval-bin-1", msys(third)}, ":")

	for in, want := range map[string]string{
		"which: no docker in (" + bash + ")\n": "which: no docker in (<PATH>)\n",
		"which: no docker in (/a (x86)/b:/c)":  "which: no docker in (<PATH>)",
		"PATH=" + raw + "\nnext":               "PATH=<PATH>\nnext",
		"echo: " + bash + " done":              "echo: <PATH> done",
		// Lists that are not the machine's PATH stay.
		"ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin":     "ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"PATH=/opt/homebrew/bin:/snap/bin:/usr/games:/usr/bin":        "PATH=/opt/homebrew/bin:/snap/bin:/usr/games:/usr/bin",
		"at 10:30:45, a:b:c and /x:/y:/z":                             "at 10:30:45, a:b:c and /x:/y:/z",
		"two: " + tilde(homeBin) + ":/usr/bin":                        "two: ~/bin:/usr/bin",
		"github.com/a/b:github.com/c/d:golang.org/x/tools":            "github.com/a/b:github.com/c/d:golang.org/x/tools",
		"https://a.example/x;https://b.example/y;https://c.example/z": "https://a.example/x;https://b.example/y;https://c.example/z",
		// which's output cut short: the text after it stays.
		"which: no docker in (/usr/bin:/bin\nlater text (with parens) and more)": "which: no docker in (/usr/bin:/bin\nlater text (with parens) and more)",
		"which: no docker in (/usr/bin:/bin\" then \"x)":                         "which: no docker in (/usr/bin:/bin\" then \"x)",
	} {
		if got := string(s.paths([]byte(in))); got != want {
			t.Errorf("paths(%q)\n = %q\nwant %q", in, got, want)
		}
	}

	// In a transcript, escaped in JSON.
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": "PATH is " + raw + "\nok"}})
	out := s.jsonl(line)
	var ev map[string]any
	if err := json.Unmarshal(out, &ev); err != nil {
		t.Fatalf("scrubbed line is not JSON: %v\n%s", err, out)
	}
	if got := ev["message"].(map[string]any)["content"]; got != "PATH is <PATH>\nok" {
		t.Errorf("transcript content = %q", got)
	}

	// which cut short in a transcript, its ) in another field.
	line, _ = json.Marshal(map[string]any{"type": "user", "a": "which: no docker in (/usr/bin:/c/x", "b": "ok)"})
	out = s.jsonl(line)
	ev = nil
	if err := json.Unmarshal(out, &ev); err != nil {
		t.Fatalf("scrubbed line is not JSON: %v\n%s", err, out)
	}
	if ev["a"] != "which: no docker in (/usr/bin:/c/x" || ev["b"] != "ok)" {
		t.Errorf("cut-short which in JSON = %s", out)
	}

	// leak: 2 of the machine's own entries, anywhere.
	if what := s.leak([]byte("see " + vendor + " and " + spaced)); what != "the machine's PATH" {
		t.Errorf("leak = %q, want the machine's PATH", what)
	}
	if what := s.leak([]byte("see " + vendor + " and /usr/bin, /usr/local/go/bin, /opt/homebrew/bin")); what != "" {
		t.Errorf("leak = %q on one own entry, want none", what)
	}
	if what := s.leak(s.text([]byte("PATH=" + raw))); what != "" {
		t.Errorf("leak = %q after scrubbing", what)
	}
}

// TestScrubWindowsPATH is a Windows PATH, as cmd and PowerShell print it.
func TestScrubWindowsPATH(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-letter paths")
	}
	home := `C:\Users\Someone`
	entries := []string{
		home + `\bin`,
		`C:\Program Files\Git\cmd`,
		`C:\Program Files\Go\bin`,
		`C:\Windows\system32`,
		`C:\Windows`,
		home + `\Local\Programs\Tool\bin`,
		`D:\tools\fakecli`,
	}
	win := strings.Join(entries, ";")
	t.Setenv("PATH", win)
	t.Setenv("SystemRoot", `C:\Windows`)
	s := newScrubberFor(scrubEnv{home: home, path: filepath.SplitList(os.Getenv("PATH"))})
	for in, want := range map[string]string{
		"Path=" + win + "\r\n":                       "Path=<PATH>\r\n",
		"Path=" + strings.ReplaceAll(win, `\`, `\\`): "Path=<PATH>",
		// An entry not on PATH, with a space, does not split the list.
		"Path=" + entries[0] + `;C:\Some Unknown Dir\x;` + strings.Join(entries[1:], ";"):                              "Path=<PATH>",
		"Path=" + strings.Join(entries[:2], ";") + `;C:\Unknown Space\y;` + entries[6]:                                 "Path=<PATH>",
		"/c/Users/Someone/bin:/c/Program Files/Git/cmd:/mingw64/bin:/usr/bin:/c/Program Files/Go/bin:/d/tools/fakecli": "<PATH>",
		// 2 own entries are enough.
		`C:\Program Files\Git\cmd;C:\Program Files\Go\bin`:       "<PATH>",
		`C:\Windows\system32;C:\Windows;C:\Program Files\Go\bin`: "<PATH>",
		// System dirs only stay.
		`C:\Windows\system32;C:\Windows`: `C:\Windows\system32;C:\Windows`,
	} {
		if got := string(s.paths([]byte(in))); got != want {
			t.Errorf("paths(%q)\n = %q\nwant %q", in, got, want)
		}
	}
}

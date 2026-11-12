package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

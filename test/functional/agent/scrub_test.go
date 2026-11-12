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

func TestScrubTranscript(t *testing.T) {
	home := fakeHome(t)
	work := t.TempDir()
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
		"{{HOME}}", jsonEscaped(t, home), "{{WORK}}", jsonEscaped(t, work),
		"{{FWD}}", fwd, "{{MSYS}}", msys, "{{SLUG}}", slug,
	).Replace(strings.ReplaceAll(string(raw), "\r\n", "\n"))

	s := newScrubber(work)
	out := string(s.jsonl([]byte(in)))

	if what := s.leak([]byte(out), "fake.person@example.net"); what != "" {
		t.Errorf("scrubbed transcript still holds %s:\n%s", what, out)
	}
	for _, gone := range []string{
		"fake.person@example.net", "Co-Authored-By", "Fake Person", "Fake Org",
		"00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b",
		"memory_paths", "userID", "fake-user-id", "oauthAccount", "userEmail", `"path"`,
	} {
		if strings.Contains(out, gone) {
			t.Errorf("scrubbed transcript still holds %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{
		`"cwd":"<run>"`, `"cwd":"~"`, "eval@example.com", "mail <email>",
		"see ~/a, ~/b and ~-c", "12345678901234567890", `"name":"fake-plugin"`,
		`"type":"credential_org"`, "not JSON: ~",
	} {
		if !strings.Contains(out, kept) {
			t.Errorf("scrubbed transcript lacks %q:\n%s", kept, out)
		}
	}
	if !s.orgs["00000000-0000-4000-8000-00000000000a"] {
		t.Errorf("organizationUuid not recorded: %v", s.orgs)
	}
	if what := s.leak([]byte(in), ""); what == "" {
		t.Error("leak found nothing in the raw transcript")
	}
}

func TestCheckLeaks(t *testing.T) {
	home := fakeHome(t)
	dir := t.TempDir()
	base := filepath.Join(dir, "go-logging-stele-1")
	if err := os.MkdirAll(base+".setup", 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(base+".diff", []byte("clean"), 0o644)
	os.WriteFile(filepath.Join(dir, "go-logging-stele-10.diff"), []byte(home), 0o644) // another run
	s := newScrubber("")
	if err := s.checkLeaks(base, "fake.person@example.net"); err != nil {
		t.Errorf("checkLeaks on clean files: %v", err)
	}
	os.WriteFile(filepath.Join(base+".setup", "CLAUDE.md"), []byte("ask FAKE.person@example.net"), 0o644)
	if err := s.checkLeaks(base, "fake.person@example.net"); err == nil || !strings.Contains(err.Error(), "git user.email") {
		t.Errorf("checkLeaks = %v, want the git email found", err)
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

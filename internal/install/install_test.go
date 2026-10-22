package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/source/stelemd"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHeaderParsesAsNoRules(t *testing.T) {
	h := Header("v1.2.0")
	if !strings.Contains(string(h), "v1.2.0") {
		t.Errorf("header lacks the version:\n%s", h)
	}
	rules, err := stelemd.Parse(h)
	if err != nil || len(rules) != 0 {
		t.Errorf("Parse(header) = %d rules, %v; want none", len(rules), err)
	}
	rules, err = stelemd.Parse(append(h, "\nscope: [\"**/*.go\"]\n\nA rule.\n"...))
	if err != nil || len(rules) != 1 {
		t.Errorf("Parse(header + rule) = %d rules, %v; want 1", len(rules), err)
	}
}

func TestInit(t *testing.T) {
	root := t.TempDir()
	changes, _, err := Init(root, "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"added stele hooks (PreToolUse, PostToolUse) to " + filepath.Join(".claude", "settings.json"),
		"created .stele/.gitignore",
		"created stele.md",
	}
	if diff := cmp.Diff(want, changes); diff != "" {
		t.Errorf("changes mismatch (-want +got):\n%s", diff)
	}
	if got := read(t, filepath.Join(root, "stele.md")); got != string(Header("v1.2.0")) {
		t.Errorf("stele.md = %q", got)
	}
	if got := read(t, filepath.Join(root, ".stele", ".gitignore")); got != "*\n" {
		t.Errorf(".stele/.gitignore = %q", got)
	}
	if got := read(t, filepath.Join(root, ".claude", "settings.json")); !strings.Contains(got, "stele hook --installed=v1.2.0") {
		t.Errorf("settings.json = %s", got)
	}

	// A second init writes nothing.
	settings := filepath.Join(root, ".claude", "settings.json")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(settings, old, old)
	changes, _, err = Init(root, "v1.2.0")
	if err != nil || len(changes) != 0 {
		t.Errorf("Init again = %q, %v; want no changes", changes, err)
	}
	if info, _ := os.Stat(settings); !info.ModTime().Equal(old) {
		t.Errorf("Init again rewrote settings.json")
	}

	// A new version restamps the hooks only.
	changes, _, err = Init(root, "v1.3.0")
	if err != nil || len(changes) != 1 || !strings.Contains(read(t, settings), "--installed=v1.3.0") {
		t.Errorf("Init v1.3.0 = %q, %v", changes, err)
	}
}

func TestInitKeepsAnExistingSteleMD(t *testing.T) {
	root := t.TempDir()
	mine := "My own rule.\n"
	write(t, filepath.Join(root, "stele.md"), mine)
	_, notes, err := Init(root, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, "stele.md")); got != mine {
		t.Errorf("stele.md changed to %q", got)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, "\n"), "kept the existing stele.md") {
		t.Errorf("notes = %q", notes)
	}
}

func TestInitKeepsUserSettings(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	write(t, settings, "{\n  \"model\": \"opus\"\n}\n")
	if _, _, err := Init(root, "v1"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, settings); !strings.HasPrefix(got, "{\n  \"model\": \"opus\",\n  \"hooks\"") {
		t.Errorf("settings.json = %s", got)
	}
}

func TestInitRefusesMalformedSettings(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	bad := "{\n  // mine\n}\n"
	write(t, settings, bad)
	_, _, err := Init(root, "v1")
	if err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("Init = %v, want an error naming settings.json", err)
	}
	if read(t, settings) != bad || exists(filepath.Join(root, "stele.md")) || exists(filepath.Join(root, ".stele")) {
		t.Errorf("Init wrote something after refusing")
	}
	if _, _, err := Uninstall(root); err == nil {
		t.Errorf("Uninstall over malformed settings succeeded")
	}
}

func TestInitRefusesSymlinkedSettings(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "settings.json")
	write(t, target, "{}\n")
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	if err := os.Symlink(target, filepath.Join(root, ".claude", "settings.json")); err != nil {
		t.Skipf("cannot make symlinks here: %v", err)
	}
	_, _, err := Init(root, "v1")
	if err == nil || !strings.Contains(err.Error(), "stele hook --installed=v1") {
		t.Errorf("Init = %v, want an error showing the hooks to add by hand", err)
	}
	if read(t, target) != "{}\n" {
		t.Errorf("the link's target changed")
	}
}

func TestInitKeepsSettingsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	write(t, settings, "{}\n")
	os.Chmod(settings, 0o600)
	if _, _, err := Init(root, "v1"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(settings); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestInitNotesALocalSteleHook(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"stele hook"}]}]}}`)
	_, notes, err := Init(root, "v1")
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), "settings.local.json") {
		t.Errorf("Init = %q, %v; want a note about settings.local.json", notes, err)
	}
}

func TestUninstall(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	write(t, settings, "{\n  \"model\": \"opus\"\n}\n")
	write(t, filepath.Join(root, ".claude", "agents", "mine.md"), "mine")
	if _, _, err := Init(root, "v1"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "stele.lock"), "{}")
	write(t, filepath.Join(root, "stele.json"), "{}")
	write(t, filepath.Join(root, ".stele", "state", "s.json"), "x")

	changes, _, err := Uninstall(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"removed stele hooks from " + filepath.Join(".claude", "settings.json"), "removed .stele/"}
	if diff := cmp.Diff(want, changes); diff != "" {
		t.Errorf("changes mismatch (-want +got):\n%s", diff)
	}
	if got := read(t, settings); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("settings.json = %q", got)
	}
	for _, kept := range []string{"stele.md", "stele.lock", "stele.json", filepath.Join(".claude", "agents", "mine.md")} {
		if !exists(filepath.Join(root, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
	if exists(filepath.Join(root, ".stele")) {
		t.Errorf(".stele/ is still there")
	}

	// Nothing left to remove.
	changes, _, err = Uninstall(root)
	if err != nil || len(changes) != 0 {
		t.Errorf("Uninstall again = %q, %v", changes, err)
	}
}

func TestUninstallRemovesSettingsLeftEmpty(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Init(root, "v1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Uninstall(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".claude")) {
		t.Errorf(".claude/ is still there")
	}
	if !exists(filepath.Join(root, "stele.md")) {
		t.Errorf("stele.md was removed")
	}
}

// Windows junctions need no privilege, unlike symlinks, so a clone can plant
// one wherever it is checked out.
func TestUninstallNeverFollowsJunctions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows only")
	}
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	write(t, keep, "keep")
	junction := func(link string) {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Skipf("mklink /J: %v: %s", err, out)
		}
	}

	root := t.TempDir()
	junction(filepath.Join(root, ".stele"))
	if _, _, err := Uninstall(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".stele")) || !exists(keep) {
		t.Errorf("junction kept or target removed")
	}

	root = t.TempDir()
	os.MkdirAll(filepath.Join(root, ".stele"), 0o755)
	junction(filepath.Join(root, ".stele", "out"))
	if _, _, err := Uninstall(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".stele")) || !exists(keep) {
		t.Errorf(".stele/ kept or the junction's target removed")
	}
}

func TestUninstallNeverFollowsLinks(t *testing.T) {
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	write(t, keep, "keep")

	// .stele/ itself a link: only the link goes.
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".stele")); err != nil {
		t.Skipf("cannot make symlinks here: %v", err)
	}
	if _, _, err := Uninstall(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".stele")) || !exists(keep) {
		t.Errorf("link kept or target removed")
	}

	// A link inside .stele/: its target stays.
	root = t.TempDir()
	os.MkdirAll(filepath.Join(root, ".stele"), 0o755)
	if err := os.Symlink(outside, filepath.Join(root, ".stele", "out")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Uninstall(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".stele")) || !exists(keep) {
		t.Errorf(".stele/ kept or the link's target removed")
	}
}

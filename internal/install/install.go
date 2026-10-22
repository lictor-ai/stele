package install

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
)

// steleHeader is the comment a new stele.md starts with. stele.md skips HTML
// comments before a rule (D56), so it is never a rule.
//
//go:embed assets/stele-header.md.tmpl
var steleHeader string

var headerTemplate = template.Must(template.New("stele-header").Parse(steleHeader))

// Header renders the comment a new stele.md starts with, stamped with the
// stele version that wrote it.
func Header(version string) []byte {
	var b strings.Builder
	if err := headerTemplate.Execute(&b, struct{ Version string }{version}); err != nil {
		panic(err) // the template is compiled in and tested
	}
	return []byte(b.String())
}

// maxSettings caps the settings file init reads.
const maxSettings = 1 << 20

// Init sets stele up under root (spec §6, ADR-0004): the Claude Code hooks
// before and after a write, stamped with version, .stele/ and, only when
// there is none, stele.md. It checks the settings file before writing
// anything, so a file it cannot read leaves the repository untouched. changes
// say what it wrote; notes what the user should know.
func Init(root, version string) (changes, notes []string, err error) {
	settings := filepath.Join(root, claudecode.SettingsFile)
	data, mode, err := readSettings(settings, version)
	if err != nil {
		return nil, nil, err
	}
	out, changed, notes, err := claudecode.Install(data, version)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w; fix it and run stele init again", claudecode.SettingsFile, err)
	}

	if changed {
		if err := safefile.Dir(filepath.Dir(settings)); err != nil {
			return nil, nil, err
		}
		if err := safefile.ReplacePerm(settings, out, mode); err != nil {
			return nil, nil, err
		}
		changes = append(changes, "added stele hooks (PreToolUse, PostToolUse) to "+claudecode.SettingsFile)
	}

	gitignore := filepath.Join(root, index.Dir, ".gitignore")
	had := exists(gitignore)
	if err := index.EnsureDir(root); err != nil {
		return changes, notes, err
	}
	if !had {
		changes = append(changes, "created "+index.Dir+"/.gitignore")
	}

	// The one file stele creates and never writes again (D38): Create fails
	// rather than replace whatever is there.
	err = safefile.Create(filepath.Join(root, "stele.md"), Header(version), 0o644)
	switch {
	case errors.Is(err, fs.ErrExist):
		notes = append(notes, "kept the existing stele.md")
	case err != nil:
		return changes, notes, err
	default:
		changes = append(changes, "created stele.md")
	}

	local := filepath.Join(filepath.Dir(settings), "settings.local.json")
	if l, err := safefile.ReadRegular(local, maxSettings); err == nil && claudecode.HasSteleHook(l) {
		notes = append(notes, filepath.Join(".claude", "settings.local.json")+" also runs a stele hook: remove it there, or stele runs twice")
	}
	return changes, notes, nil
}

// Uninstall removes what Init wrote: stele's hooks, and .stele/ with
// everything stele keeps there. It leaves stele.md, stele.lock, stele.json
// and every other file, and a settings file it cannot read entirely alone.
func Uninstall(root string) (changes, notes []string, err error) {
	settings := filepath.Join(root, claudecode.SettingsFile)
	data, mode, err := readSettings(settings, "")
	if err != nil {
		return nil, nil, err
	}
	out, removed, empty, err := claudecode.Uninstall(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w; nothing was removed", claudecode.SettingsFile, err)
	}

	// The hooks go first, so a running session stops recreating .stele/.
	switch {
	case removed == 0:
	case empty:
		if err := os.Remove(settings); err != nil {
			return nil, nil, err
		}
		os.Remove(filepath.Dir(settings)) // only when nothing else is in it
		changes = append(changes, "removed stele hooks from "+claudecode.SettingsFile)
	default:
		if err := safefile.ReplacePerm(settings, out, mode); err != nil {
			return nil, nil, err
		}
		changes = append(changes, "removed stele hooks from "+claudecode.SettingsFile)
	}

	dir := filepath.Join(root, index.Dir)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return changes, nil, nil
	case err != nil:
		return changes, nil, err
	case !info.IsDir():
		// A link, or a junction, that a clone committed: only the link goes.
		err = os.Remove(dir)
	default:
		// RemoveAll removes links inside, never what they point to.
		err = os.RemoveAll(dir)
	}
	if err != nil {
		return changes, nil, err
	}
	return append(changes, "removed "+index.Dir+"/"), nil, nil
}

// readSettings reads Claude Code's settings file, missing being empty, and
// the mode to write it back with. A link is refused: replacing it would
// replace the user's link with a file. The error then shows the hooks for
// version, to add by hand.
func readSettings(path, version string) (data []byte, mode os.FileMode, err error) {
	data, err = safefile.ReadRegular(path, maxSettings)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0o644, nil
	case errors.Is(err, safefile.ErrNotRegular):
		msg := fmt.Sprintf("%s is not a plain file (a symlink?): stele will not replace it", claudecode.SettingsFile)
		if version != "" {
			hooks, _, _, _ := claudecode.Install(nil, version)
			msg += "; add these hooks to it by hand:\n" + string(hooks)
		}
		return nil, 0, errors.New(msg)
	case err != nil:
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

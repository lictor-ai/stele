package install

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

var (
	semverRE = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	// golang.org/x/mod/module's pseudoVersionRE, without build metadata:
	// Released refuses any "+".
	pseudoRE = regexp.MustCompile(`^v[0-9]+\.(0\.0-|\d+\.\d+-([^+]*\.)?0\.)\d{14}-[A-Za-z0-9]+$`)
)

// Released reports whether v is a tagged release: vX.Y.Z or vX.Y.Z-pre, not
// a pseudo-version, and no +build (Go adds +dirty to a build with local
// changes, which is not the tagged code). Anything else stamps as devel.
func Released(v string) bool { return semverRE.MatchString(v) && !pseudoRE.MatchString(v) }

// Init sets stele up under root (spec §6, ADR-0004): the Claude Code hooks
// before and after a write, stamped with version when it is a release and
// with devel otherwise, .stele/ and, only when
// there is none, stele.md. Every check comes before the first write, so a
// refusal leaves the repository untouched. changes say what it wrote; notes
// what the user should know.
func Init(root, version string) (changes, notes []string, err error) {
	settings := filepath.Join(root, claudecode.SettingsFile)
	claudeDir := filepath.Dir(settings)
	steleDir := filepath.Join(root, index.Dir)
	steleMD := filepath.Join(root, "stele.md")

	// A build from a commit stamps devel, so builds from two commits agree
	// and the committed settings do not change with every commit (#41).
	stamp := version
	if !Released(stamp) {
		stamp = "devel"
	}

	// Check.
	hooks, _, _, _ := claudecode.Install(nil, stamp, claudecode.Switches{})
	byHand := fmt.Sprintf("; add these hooks to %s by hand:\n%s", claudecode.SettingsFile, hooks)
	if err := plainDir(claudeDir); err != nil {
		return nil, nil, fmt.Errorf("%w: stele will not write through it%s", err, byHand)
	}
	if err := plainDir(steleDir); err != nil {
		return nil, nil, fmt.Errorf("%w: stele keeps its files there; move it away and run stele init again", err)
	}
	data, mode, err := readSettings(settings)
	if errors.Is(err, safefile.ErrNotRegular) {
		return nil, nil, fmt.Errorf("%s is not a plain file (a symlink?): stele will not replace it%s", claudecode.SettingsFile, byHand)
	}
	if err != nil {
		return nil, nil, err
	}
	if v, _, ok := claudecode.Installed(data); ok && Released(v) && !Released(version) {
		stamp = v // a build from a commit never replaces a release's stamp
	}
	out, changed, notes, err := claudecode.Install(data, stamp, claudecode.Switches{})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w; fix it and run stele init again", claudecode.SettingsFile, err)
	}
	createMD := !exists(steleMD)
	hadGitignore := exists(filepath.Join(steleDir, ".gitignore"))

	// Write: the settings last, so the hooks never run without the rest.
	if err := index.EnsureDir(root); err != nil {
		return changes, notes, err
	}
	if !hadGitignore {
		changes = append(changes, "created "+index.Dir+"/.gitignore")
	}
	if createMD {
		// The one file stele creates and never writes again (D38): Create
		// fails rather than replace whatever appeared meanwhile.
		err := safefile.Create(steleMD, Header(stamp), 0o644)
		switch {
		case errors.Is(err, fs.ErrExist):
			notes = append(notes, "kept the existing stele.md")
		case err != nil:
			return changes, notes, err
		default:
			changes = append(changes, "created stele.md")
		}
	} else {
		notes = append(notes, "kept the existing stele.md")
	}
	if changed {
		if err := safefile.Dir(claudeDir); err != nil {
			return changes, notes, err
		}
		if err := safefile.ReplacePerm(settings, out, mode); err != nil {
			return changes, notes, err
		}
		changes = append(changes, "added stele hooks ("+strings.Join(claudecode.Events(), ", ")+") to "+claudecode.SettingsFile)
	}

	local := filepath.Join(claudeDir, "settings.local.json")
	if l, err := safefile.ReadRegular(local, maxSettings); err == nil && claudecode.OtherSteleHook(l, stamp, claudecode.Switches{}) {
		notes = append(notes, filepath.Join(".claude", "settings.local.json")+" runs another stele hook: remove it there, or stele runs twice")
	}
	return changes, notes, nil
}

// Uninstall removes what Init and stele compile wrote: stele's hooks, the
// rules file Claude Code loads (#68), and .stele/ with everything stele keeps
// there. It leaves stele.md, stele.lock, stele.json and every other file, and
// a settings file it cannot read entirely alone.
func Uninstall(root string) (changes, notes []string, err error) {
	settings := filepath.Join(root, claudecode.SettingsFile)
	rulesFile := filepath.Join(root, filepath.FromSlash(claudecode.RulesFile))
	for _, dir := range []string{filepath.Dir(settings), filepath.Dir(rulesFile)} {
		if err := plainDir(dir); err != nil {
			return nil, nil, fmt.Errorf("%w: stele will not write through it; remove its stele hooks and %s by hand, nothing was removed", err, claudecode.RulesFile)
		}
	}
	data, mode, err := readSettings(settings)
	if errors.Is(err, safefile.ErrNotRegular) {
		return nil, nil, fmt.Errorf("%s is not a plain file (a symlink?): remove its stele hooks by hand, nothing was removed", claudecode.SettingsFile)
	}
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

	// The rules file goes when stele wrote it, by its header, and so does a
	// link a clone committed there: only the link, never what it points to.
	// A file without the header is the user's.
	if info, err := os.Lstat(rulesFile); err == nil {
		ours := info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
		if info.Mode().IsRegular() {
			data, err := safefile.ReadRegular(rulesFile, claudecode.MaxRulesFile)
			ours = err == nil && claudecode.OwnedRules(data)
		}
		if !ours {
			notes = append(notes, claudecode.RulesFile+" is not stele's: left it")
		} else if err := os.Remove(rulesFile); err != nil {
			return changes, notes, err
		} else {
			changes = append(changes, "removed "+claudecode.RulesFile)
		}
	}
	os.Remove(filepath.Dir(rulesFile)) // only when nothing else is in it
	os.Remove(filepath.Dir(settings))

	dir := filepath.Join(root, index.Dir)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return changes, notes, nil
	case err != nil:
		return changes, notes, err
	case info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
		// A link, or a junction, that a clone committed: only the link goes.
		err = os.Remove(dir)
	case !info.IsDir():
		return changes, append(notes, index.Dir+" is not a directory stele made: left it"), nil
	default:
		// RemoveAll removes links inside, never what they point to.
		err = os.RemoveAll(dir)
	}
	if err != nil {
		return changes, notes, err
	}
	return append(changes, "removed "+index.Dir+"/"), notes, nil
}

// plainDir checks that path is missing or a real directory: never a link or
// a junction, which would take stele's writes somewhere else.
func plainDir(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir() || info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is not a plain directory (a link?)", path)
	}
	return nil
}

// readSettings reads Claude Code's settings file, missing being empty, and
// the mode to write it back with. A link is safefile.ErrNotRegular:
// replacing it would replace the user's link with a file.
func readSettings(path string) (data []byte, mode os.FileMode, err error) {
	data, err = safefile.ReadRegular(path, maxSettings)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0o644, nil
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

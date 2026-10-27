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

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/enforce"
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

// Older reports whether release a comes before release b, by semver
// precedence: major, minor and patch as numbers, then a pre-release before
// the release it leads to. Both must be Released.
func Older(a, b string) bool { return compareSemver(a, b) < 0 }

func compareSemver(a, b string) int {
	ma, mb := semverRE.FindStringSubmatch(a), semverRE.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumbers(ma[i], mb[i]); c != 0 {
			return c
		}
	}
	pa, pb := strings.TrimPrefix(ma[4], "-"), strings.TrimPrefix(mb[4], "-")
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1 // a release comes after its pre-releases
	case pb == "":
		return -1
	}
	fa, fb := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < len(fa) && i < len(fb); i++ {
		if c := compareIdent(fa[i], fb[i]); c != 0 {
			return c
		}
	}
	return compareInts(len(fa), len(fb))
}

// compareIdent orders two pre-release identifiers: numbers as numbers,
// before any word; words in ASCII order.
func compareIdent(a, b string) int {
	na, nb := numeric(a), numeric(b)
	switch {
	case na && nb:
		return compareNumbers(a, b)
	case na:
		return -1
	case nb:
		return 1
	}
	return strings.Compare(a, b)
}

func numeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// compareNumbers orders decimal strings without leading zeros, of any
// length.
func compareNumbers(a, b string) int {
	if c := compareInts(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Set is the switches stele init was asked to change: nil keeps what the
// installed hooks carry, so running init again never turns a hook back on.
type Set struct{ Remind, Check *bool }

// apply returns the installed switches with s applied.
func (s Set) apply(sw adapter.Switches) adapter.Switches {
	if s.Remind != nil {
		sw.RemindOff = !*s.Remind
	}
	if s.Check != nil {
		sw.CheckOff = !*s.Check
	}
	return sw
}

// BackstopNote is init's note for a stele.json that still sets backstop,
// which nothing reads since ADR-0008. stele never edits the file (D38).
const BackstopNote = "stele.json: backstop is no longer read; use stele init --remind=off"

// Init sets stele up under root for agent a (spec §6, ADR-0004): its hooks
// before and after a write, stamped with version when it is a release and
// with devel otherwise and carrying the switches set asks for (ADR-0006),
// .stele/ and, only when there is none, stele.md. Every check comes before
// the first write, so a refusal leaves the repository untouched. changes say
// what it wrote; notes what the user should know.
func Init(a adapter.Adapter, root, version string, set Set) (changes, notes []string, err error) {
	settings := filepath.Join(root, a.SettingsFile())
	settingsDir := filepath.Dir(settings)
	steleDir := filepath.Join(root, index.Dir)
	steleMD := filepath.Join(root, "stele.md")

	// A build from a commit stamps devel, so builds from two commits agree
	// and the committed settings do not change with every commit (#41).
	stamp := version
	if !Released(stamp) {
		stamp = "devel"
	}

	// Check.
	hooks, _, _, _ := a.Install(nil, stamp, set.apply(adapter.Switches{}))
	byHand := fmt.Sprintf("; add these hooks to %s by hand:\n%s", a.SettingsFile(), hooks)
	if err := plainDir(settingsDir); err != nil {
		return nil, nil, fmt.Errorf("%w: stele will not write through it%s", err, byHand)
	}
	if err := plainDir(steleDir); err != nil {
		return nil, nil, fmt.Errorf("%w: stele keeps its files there; move it away and run stele init again", err)
	}
	data, mode, err := readSettings(settings)
	if errors.Is(err, safefile.ErrNotRegular) {
		return nil, nil, fmt.Errorf("%s is not a plain file (a symlink?): stele will not replace it%s", a.SettingsFile(), byHand)
	}
	if err != nil {
		return nil, nil, err
	}
	v, installed, had := a.Installed(data)
	if had && Released(v) && !Released(version) {
		stamp = v // a build from a commit never replaces a release's stamp
	}
	sw := set.apply(installed)
	out, changed, notes, err := a.Install(data, stamp, sw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w; fix it and run stele init again", a.SettingsFile(), err)
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
		if err := safefile.Dir(settingsDir); err != nil {
			return changes, notes, err
		}
		if err := safefile.ReplacePerm(settings, out, mode); err != nil {
			return changes, notes, err
		}
		if had {
			changes = append(changes, "updated stele hooks in "+a.SettingsFile())
		} else {
			changes = append(changes, "added stele hooks ("+strings.Join(a.Hooks(), ", ")+") to "+a.SettingsFile())
		}
	}

	// The agent's other settings files, such as a local one beside them.
	for _, p := range a.Settings() {
		if p == filepath.ToSlash(a.SettingsFile()) {
			continue
		}
		other := filepath.FromSlash(p)
		if l, err := safefile.ReadRegular(filepath.Join(root, other), maxSettings); err == nil && a.OtherSteleHook(l, stamp, sw) {
			notes = append(notes, other+" runs another stele hook: remove it there, or stele runs twice")
		}
	}
	if enforce.SetsBackstop(root) {
		notes = append(notes, BackstopNote)
	}
	return changes, notes, nil
}

// InstalledSwitches returns the switches agent a's stele hooks under root
// carry. ok is false when there are none, or the settings cannot be read.
func InstalledSwitches(a adapter.Adapter, root string) (sw adapter.Switches, ok bool) {
	data, _, err := readSettings(filepath.Join(root, a.SettingsFile()))
	if err != nil {
		return adapter.Switches{}, false
	}
	_, sw, ok = a.Installed(data)
	return sw, ok
}

// Uninstall removes what Init and stele compile wrote for agent a: stele's
// hooks, the rules file the agent loads (#68), and .stele/ with everything
// stele keeps there. It leaves stele.md, stele.lock, stele.json and every
// other file, and a settings file it cannot read entirely alone.
func Uninstall(a adapter.Adapter, root string) (changes, notes []string, err error) {
	settings := filepath.Join(root, a.SettingsFile())
	rulesFile := filepath.Join(root, filepath.FromSlash(a.RulesFile()))
	for _, dir := range []string{filepath.Dir(settings), filepath.Dir(rulesFile)} {
		if err := plainDir(dir); err != nil {
			return nil, nil, fmt.Errorf("%w: stele will not write through it; remove its stele hooks and %s by hand, nothing was removed", err, a.RulesFile())
		}
	}
	data, mode, err := readSettings(settings)
	if errors.Is(err, safefile.ErrNotRegular) {
		return nil, nil, fmt.Errorf("%s is not a plain file (a symlink?): remove its stele hooks by hand, nothing was removed", a.SettingsFile())
	}
	if err != nil {
		return nil, nil, err
	}
	out, removed, empty, err := a.Uninstall(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w; nothing was removed", a.SettingsFile(), err)
	}

	// The hooks go first, so a running session stops recreating .stele/.
	switch {
	case removed == 0:
	case empty:
		if err := os.Remove(settings); err != nil {
			return nil, nil, err
		}
		os.Remove(filepath.Dir(settings)) // only when nothing else is in it
		changes = append(changes, "removed stele hooks from "+a.SettingsFile())
	default:
		if err := safefile.ReplacePerm(settings, out, mode); err != nil {
			return nil, nil, err
		}
		changes = append(changes, "removed stele hooks from "+a.SettingsFile())
	}

	// The rules file goes when stele wrote it, by its header, and so does a
	// link a clone committed there: only the link, never what it points to.
	// A file without the header is the user's.
	if info, err := os.Lstat(rulesFile); err == nil {
		ours := info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
		if info.Mode().IsRegular() {
			data, err := safefile.ReadRegular(rulesFile, a.MaxRulesFile())
			ours = err == nil && a.OwnedRules(data)
		}
		if !ours {
			notes = append(notes, a.RulesFile()+" is not stele's: left it")
		} else if err := os.Remove(rulesFile); err != nil {
			return changes, notes, err
		} else {
			changes = append(changes, "removed "+a.RulesFile())
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

// readSettings reads the agent's settings file, missing being empty, and
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

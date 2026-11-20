package shell

import "strings"

// The files and folders touch, mkdir and git make. They hold no content a
// rule is about, so they matter only where a new file or folder changes
// stele: its own files, and a .git below a project's root, which ends the
// project there.

// touch adds the files of touch, unless -c or --no-create says it makes
// none.
func (p *parser) touch(args []word) {
	var ops []word
	for i := 0; i < len(args); i++ {
		a := args[i]
		t := a.text
		switch {
		case t == "--":
			ops = append(ops, args[i+1:]...)
			i = len(args)
		case a.dynamic || !strings.HasPrefix(t, "-") || t == "-":
			ops = append(ops, a)
		case t == "--no-create":
			return
		case t == "--date" || t == "--reference" || t == "--time":
			i++
		case strings.HasPrefix(t, "--"):
		default:
			// A cluster, such as -ac or -d DATE: d, r and t take the rest,
			// or the next word.
		cluster:
			for j := 1; j < len(t); j++ {
				switch t[j] {
				case 'c':
					return
				case 'd', 'r', 't':
					if j == len(t)-1 {
						i++
					}
					break cluster
				}
			}
		}
	}
	for _, a := range ops {
		p.add(a, Create)
	}
}

// gitAdd adds the .git folders, or bare repositories, a git command makes.
func (p *parser) gitAdd(args []word) {
	made, unknown := gitMakes(args)
	p.res.unknown += unknown
	for _, w := range made {
		p.add(w, Create)
	}
}

// gitMakes reads the arguments of a git command, after git, and returns the
// .git folders it makes, or the repository itself when it is bare, as paths
// written relative to the command's directory unless absolute: git init,
// clone, worktree add and submodule add. -C before the command moves the
// directory they are relative to. unknown counts those it cannot place: a
// word stele cannot know, a name it cannot derive, or --git-dir and
// --work-tree, which put the repository elsewhere. Any other git command
// makes none.
func gitMakes(args []word) (made []word, unknown int) {
	base, baseOK := "", true // -C's directories, joined
	elsewhere, bare := false, false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		t := a.text
		if a.dynamic || !strings.HasPrefix(t, "-") {
			break
		}
		switch {
		case t == "-C":
			i++
			if i >= len(args) || args[i].dynamic {
				baseOK = false
			} else if args[i].text != "" {
				base = gitJoin(base, args[i].text)
			}
		case t == "--git-dir", t == "--work-tree":
			i++
			elsewhere = true
		case strings.HasPrefix(t, "--git-dir="), strings.HasPrefix(t, "--work-tree="):
			elsewhere = true
		case t == "-c", t == "--namespace", t == "--config-env", t == "--super-prefix", t == "--list-cmds", t == "--attr-source":
			i++
		case t == "--exec-path", t == "--html-path", t == "--man-path", t == "--info-path",
			t == "--help", t == "-h", t == "--version", t == "-v":
			return nil, 0 // git prints a path, help or its version: no command runs
		case t == "--bare":
			bare = true
		}
	}
	if i >= len(args) || args[i].dynamic {
		return nil, 0 // no command, or one stele cannot know
	}
	sub, rest := args[i].text, args[i+1:]
	var dirs []word // relative to base
	var dirOK bool  // false: one is not known
	switch sub {
	case "init":
		ops, opts := gitOptions(rest, gitInitTakes)
		dir := word{text: "."}
		if len(ops) > 0 {
			dir = ops[0]
		}
		bare = bare || opts["--bare"] != nil
		dirs, dirOK = gitRepo(dir, bare, opts["--separate-git-dir"])
	case "clone":
		ops, opts := gitOptions(rest, gitCloneTakes)
		if len(ops) == 0 {
			return nil, 0
		}
		bare = bare || opts["--bare"] != nil || opts["--mirror"] != nil
		dir, ok := gitDest(ops, bare)
		if !ok {
			return nil, 1
		}
		dirs, dirOK = gitRepo(dir, bare, opts["--separate-git-dir"])
	case "worktree":
		if len(rest) == 0 || rest[0].dynamic || rest[0].text != "add" {
			return nil, 0
		}
		ops, _ := gitOptions(rest[1:], gitWorktreeTakes)
		if len(ops) == 0 {
			return nil, 0
		}
		dirs, dirOK = gitRepo(ops[0], false, nil)
	case "submodule":
		j := 0
		for j < len(rest) && (rest[j].text == "-q" || rest[j].text == "--quiet") {
			j++
		}
		if j >= len(rest) || rest[j].dynamic || rest[j].text != "add" {
			return nil, 0
		}
		ops, _ := gitOptions(rest[j+1:], gitSubmoduleTakes)
		if len(ops) == 0 {
			return nil, 0
		}
		dir, ok := gitDest(ops, false)
		if !ok {
			return nil, 1
		}
		dirs, dirOK = gitRepo(dir, false, nil)
	default:
		return nil, 0
	}
	if elsewhere || !baseOK || !dirOK {
		return nil, max(len(dirs), 1)
	}
	for _, d := range dirs {
		made = append(made, word{text: gitJoin(base, d.text)})
	}
	return made, 0
}

// gitRepo returns what a repository made in dir holds stele cares about:
// dir/.git, or dir itself when bare, and a separate git directory. ok is
// false when one of them is a word stele cannot know.
func gitRepo(dir word, bare bool, separate *word) (dirs []word, ok bool) {
	ok = !dir.dynamic
	if bare {
		dirs = append(dirs, dir)
	} else {
		dirs = append(dirs, word{text: inDir(strings.TrimRight(dir.text, `/\`), ".git")})
	}
	if separate != nil && !bare {
		ok = ok && !separate.dynamic
		dirs = append(dirs, *separate)
	}
	return dirs, ok
}

// gitDest is the folder git clone or git submodule add makes: the one given
// after the repository, else the repository's name, as git derives it, with
// .git added when bare. ok is false when stele cannot know it.
func gitDest(ops []word, bare bool) (word, bool) {
	if len(ops) > 1 {
		return ops[1], !ops[1].dynamic
	}
	if ops[0].dynamic {
		return word{}, false
	}
	name, ok := repoName(ops[0].text)
	if !ok {
		return word{}, false
	}
	if bare {
		name += ".git"
	}
	return word{text: name}, true
}

// repoName derives the folder git clone makes from a repository's URL or
// path, as git does: its last part, without a trailing / or /.git, and
// without .git or .bundle. A URL with no path is its host's name. ok is false
// when there is no name, or it is ~ or ~user.
func repoName(url string) (string, bool) {
	s := strings.TrimSpace(url)
	if i := strings.Index(s, "://"); i >= 0 {
		host := s[i+3:]
		if j := strings.IndexAny(host, `/`); j < 0 || strings.Trim(host[j:], "/") == "" {
			// No path: the host, without a user or a port.
			if j >= 0 {
				host = host[:j]
			}
			if k := strings.LastIndexByte(host, '@'); k >= 0 {
				host = host[k+1:]
			}
			host, _, _ = strings.Cut(host, ":")
			return host, host != ""
		}
	}
	s = strings.TrimRight(s, `/\`)
	s = strings.TrimSuffix(s, "/.git")
	s = strings.TrimSuffix(s, `\.git`)
	s = strings.TrimRight(s, `/\`)
	if i := strings.LastIndexAny(s, `/\:`); i >= 0 {
		s = s[i+1:]
	}
	if strings.HasSuffix(s, ".bundle") {
		s = strings.TrimSuffix(s, ".bundle")
	} else {
		s = strings.TrimSuffix(s, ".git")
	}
	// ~ or ~user, which git does not expand and a shell may: not a name.
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "~") || strings.ContainsAny(s, "\x00\n\r\t") {
		return "", false
	}
	return s, true
}

// gitJoin is p in base, base left as written: an absolute p, or ~, stands on
// its own.
func gitJoin(base, p string) string {
	if base == "" || p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return p
	}
	if _, abs := Abs("", p); abs || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return p
	}
	return inDir(base, p)
}

// gitOptions splits a git command's arguments into its operands and its
// options, an option with no value mapped to an empty word. takes lists the
// options whose value may be the next word; a long one may also be joined
// with =, and a short one written onto it.
func gitOptions(args []word, takes map[string]bool) (ops []word, opts map[string]*word) {
	opts = map[string]*word{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		t := a.text
		switch {
		case t == "--":
			return append(ops, args[i+1:]...), opts
		case a.dynamic || !strings.HasPrefix(t, "-") || t == "-":
			ops = append(ops, a)
		case strings.HasPrefix(t, "--") && strings.Contains(t, "="):
			name, v, _ := strings.Cut(t, "=")
			w := a
			w.text = v
			opts[name] = &w
		case takes[t]:
			w := word{dynamic: true} // no value: as if unknown
			if i+1 < len(args) {
				i++
				w = args[i]
			}
			opts[t] = &w
		default:
			opts[t] = &word{}
		}
	}
	return ops, opts
}

// The options of git init, clone, worktree add and submodule add whose value
// may be the next word.
var (
	gitInitTakes = map[string]bool{"--template": true, "--separate-git-dir": true, "-b": true, "--initial-branch": true,
		"--object-format": true, "--ref-format": true}
	gitCloneTakes = map[string]bool{"--template": true, "--separate-git-dir": true, "-o": true, "--origin": true,
		"-b": true, "--branch": true, "-u": true, "--upload-pack": true, "--reference": true, "--reference-if-able": true,
		"--depth": true, "--shallow-since": true, "--shallow-exclude": true, "-c": true, "--config": true,
		"--server-option": true, "-j": true, "--jobs": true, "--filter": true, "--bundle-uri": true, "--revision": true,
		"--ref-format": true}
	gitWorktreeTakes  = map[string]bool{"-b": true, "-B": true, "--reason": true}
	gitSubmoduleTakes = map[string]bool{"-b": true, "--branch": true, "--name": true, "--reference": true, "--depth": true}
)

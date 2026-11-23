package shell

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// PowerShellTargets returns the files a PowerShell command writes, resolved
// from cwd, in the order they appear, each once, as Targets does for bash
// (D77). Codex's shell runs PowerShell on Windows. It is best effort: what it
// cannot follow it counts as unknown, and stele check in CI is the catch.
//
// It reads redirections (> >> *> n>), after any word; Set-Content,
// Add-Content, Out-File, Tee-Object, New-Item (files, folders and links),
// mkdir, Clear-Content, Copy-Item, Move-Item, Rename-Item, Remove-Item,
// Export-Csv, Export-Clixml, Start-Transcript and Invoke-WebRequest -OutFile,
// by any alias or module-qualified name, with abbreviated and colon-joined
// parameters and the common ones; [IO.File]'s write, copy, move and delete
// methods, their Async forms, and an IO.StreamWriter made on a path. It
// follows Set-Location, Push-Location and Pop-Location, and variables set to
// a literal at the top level: one set in a block, from a command, or added
// to, is unknown from then on. A command assigned, cast, or in parentheses
// is read as a command. A parameter may start with an en dash, em dash or
// horizontal bar, as PowerShell allows. Here-strings, array literals, nested
// powershell, pwsh (-EncodedCommand too), bash, sh, zsh and cmd commands with
// a literal script, and Invoke-Expression of a literal string are read, and
// git's commands that make a repository, as in bash.
// $env:NAME and ${env:NAME} expand from the environment (the name's case
// matters where it does to the OS: on Windows not), $HOME to the user's home
// and $PWD to the directory the command has moved to, within the budget
// bash's variables share; one the command sets is unknown from then on, in a
// nested bash too: by assignment, any cmdlet on env: or variable:,
// Set-Variable and its kin, foreach, [Environment]::SetEnvironmentVariable,
// or a write after cd env: (#181). A cmd /c script with %VAR% is unknown.
// Expressions as paths ($x.Name too), splatting, $(...), other automatic variables such as
// $PSHOME, unknown variables, paths bound from the pipeline, any other
// Invoke-Expression, Start-Process and scripts are unknown or not seen.
func PowerShellTargets(command, cwd string) (ts []Target, unknown int) {
	if len(command) > maxCommand {
		return nil, 1
	}
	r := &results{seen: map[string]int{}}
	p := &psReader{res: r, vars: map[string]string{}, set: map[string]bool{}, dir: cwd, dirOK: cwd != ""}
	p.run(command)
	return r.out, r.unknown
}

// psKind is what a token is.
type psKind int

const (
	psWordK  psKind = iota // a word, quoted parts and variables in it
	psRedirK               // a redirection operator: > >> 2> *>
	psMergeK               // a stream merge, 2>&1: no file
	psGroupK               // ( … ), @( … ): what is inside
)

// psTok is one token of a statement.
type psTok struct {
	kind    psKind
	text    string // a word's text, variables as \x00name\x00
	quoted  bool   // some of it was quoted
	dynamic bool   // it holds what stele cannot know: $(...), a splat
	param   bool   // -Name, a parameter, maybe -Name:value
	comma   bool   // followed by a comma: an array goes on
	array   bool   // a group written @( … )
	group   []psStmt
}

// psStmt is one statement: its tokens, how deep in blocks it is, and
// whether it reads the pipeline.
type psStmt struct {
	toks  []psTok
	depth int
	piped bool
}

// psLexer splits a command into statements.
type psLexer struct {
	s     string
	i     int
	depth int
}

// statements reads statements up to the end, or up to a ) that closes a
// group when inGroup.
func (l *psLexer) statements(inGroup bool) []psStmt {
	var out []psStmt
	cur := psStmt{depth: l.depth}
	end := func(piped bool) {
		if len(cur.toks) > 0 {
			out = append(out, cur)
		}
		cur = psStmt{depth: l.depth, piped: piped}
	}
	for l.i < len(l.s) {
		s, i := l.s, l.i
		c := s[i]
		switch {
		case c == ')' && inGroup:
			l.i++
			end(false)
			return out
		case c == ')':
			l.i++
		case c == '\n' || c == '\r' || c == ';':
			end(false)
			l.i++
		case c == '{':
			end(false)
			l.depth++
			cur.depth = l.depth
			l.i++
		case c == '}':
			end(false)
			if l.depth > 0 {
				l.depth--
			}
			cur.depth = l.depth
			l.i++
		case c == '|' && i+1 < len(s) && s[i+1] == '|', c == '&' && i+1 < len(s) && s[i+1] == '&':
			end(false)
			l.i += 2
		case c == '|':
			end(true)
			l.i++
		case c == ' ' || c == '\t':
			l.i++
		case c == '#':
			for l.i < len(s) && s[l.i] != '\n' {
				l.i++
			}
		case c == '<' && i+1 < len(s) && s[i+1] == '#':
			j := strings.Index(s[i+2:], "#>")
			if j < 0 {
				l.i = len(s)
			} else {
				l.i += j + 4
			}
		case c == ',':
			if n := len(cur.toks); n > 0 {
				cur.toks[n-1].comma = true
			}
			l.i++
		case c == '`' && i+1 < len(s) && (s[i+1] == '\n' || s[i+1] == '\r'):
			l.i += 2 // a line continued
			if l.i < len(s) && s[l.i] == '\n' {
				l.i++
			}
		case c == '(' || (c == '@' && i+1 < len(s) && s[i+1] == '('):
			array := c == '@'
			l.i += 1 + btoi(array)
			g := l.statements(true)
			cur.toks = append(cur.toks, psTok{kind: psGroupK, group: g, array: array})
		case c == '@' && i+1 < len(s) && s[i+1] == '{':
			// A hash table: its keys and values are no command.
			end(false)
			l.depth++
			cur.depth = l.depth
			l.i += 2
		case c == '@' && i+1 < len(s) && (s[i+1] == '\'' || s[i+1] == '"') && hereOpens(s[i+2:]):
			cur.toks = append(cur.toks, l.hereString())
		case c == '@' && i+1 < len(s) && (letter(s[i+1]) || s[i+1] == '_'):
			// Splatting: @args passes what stele cannot see.
			j := i + 1
			for j < len(s) && (letter(s[j]) || s[j] == '_' || ('0' <= s[j] && s[j] <= '9')) {
				j++
			}
			cur.toks = append(cur.toks, psTok{text: s[i:j], dynamic: true})
			l.i = j
		default:
			if t, n := psRedirect(s[i:]); n > 0 {
				cur.toks = append(cur.toks, t)
				l.i += n
				continue
			}
			t, n := psWord(s[i:])
			if n == 0 {
				l.i++
				continue
			}
			cur.toks = append(cur.toks, t)
			l.i += n
		}
	}
	end(false)
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// hereOpens reports whether what follows @' or @" opens a here-string: the
// rest of its line is blank.
func hereOpens(s string) bool {
	for _, c := range s {
		switch c {
		case ' ', '\t', '\r':
		case '\n':
			return true
		default:
			return false
		}
	}
	return false
}

// hereString reads a here-string from its @: its body is every line up to
// one that starts with its closer, '@ or "@.
func (l *psLexer) hereString() psTok {
	q := l.s[l.i+1]
	nl := strings.IndexByte(l.s[l.i:], '\n')
	start := l.i + nl + 1
	t := psTok{quoted: true}
	closer := "\n" + string(q) + "@"
	body := l.s[start:]
	end := strings.Index("\n"+body, closer)
	if end < 0 {
		t.text, t.dynamic, l.i = body, true, len(l.s) // unclosed: not a value to trust
		return t
	}
	text := strings.TrimSuffix(body[:max(0, end-1)], "\r")
	if end == 0 {
		text = ""
	}
	if q == '"' {
		w, _ := psWord(`"` + strings.ReplaceAll(text, `"`, "`\"") + `"`)
		t.text, t.dynamic = w.text, w.dynamic
	} else {
		t.text = text
	}
	l.i = start + end + 2
	return t
}

// psRedirect reads a redirection operator at the start of s: > >> *> *>>,
// or a stream number before either. 2>&1 and the like merge streams.
func psRedirect(s string) (psTok, int) {
	i := 0
	if len(s) > 0 && (s[0] == '*' || ('1' <= s[0] && s[0] <= '6')) {
		i = 1
	}
	if i >= len(s) || s[i] != '>' {
		return psTok{}, 0
	}
	i++
	if i < len(s) && s[i] == '>' {
		i++
	}
	if i < len(s) && s[i] == '&' {
		j := i + 1
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		return psTok{kind: psMergeK}, j
	}
	return psTok{kind: psRedirK, text: s[:i]}, i
}

// psWord reads one word: bare text, with quoted parts, up to a space, an
// operator or a redirection. Single quotes are literal, '' a quote; double
// quotes take ` escapes and variables; outside quotes ` escapes the next
// character. A word that is only a variable ends before = so $x='a' is an
// assignment.
func psWord(s string) (psTok, int) {
	var t psTok
	var b strings.Builder
	i := 0
	for _, op := range []string{"??=", "+=", "-=", "*=", "/=", "%=", "="} {
		if strings.HasPrefix(s, op) {
			return psTok{text: op}, len(op)
		}
	}
	if n := psDash(s); n > 0 && len(s) > n && letter(s[n]) {
		// -Name, or –Name, —Name or ―Name, which PowerShell takes as -Name.
		t.param = true
		b.WriteByte('-')
		i = n
	}
	lone := func() bool { // the word so far is one variable
		w := b.String()
		return len(w) > 2 && w[0] == 0 && strings.IndexByte(w[1:], 0) == len(w)-2
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' || c == '|' || c == ',' ||
			c == '{' || c == '}' || c == '(' || c == ')':
			t.text = b.String()
			return t, i
		case c == '>' && (b.Len() > 0 || t.quoted || t.dynamic):
			t.text = b.String()
			return t, i
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			t.text = b.String()
			return t, i
		case lone() && !t.quoted && (c == '=' || (strings.ContainsRune("+-*/%?", rune(c)) && i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '?'))):
			t.text = b.String()
			return t, i
		case c == '\'':
			t.quoted = true
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						b.WriteByte('\'')
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
		case c == '"':
			t.quoted = true
			i++
			for i < len(s) && s[i] != '"' {
				switch {
				case s[i] == '`' && i+1 < len(s):
					b.WriteByte(psEscape(s[i+1]))
					i += 2
				case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
					t.dynamic = true
					i = psSkipParen(s, i+1)
				case s[i] == '$' && i+1 < len(s) && psVarStart(s[i+1]):
					name, n := psVarName(s[i+1:])
					b.WriteString("\x00" + name + "\x00")
					i += 1 + n
				default:
					b.WriteByte(s[i])
					i++
				}
			}
			i++
		case c == '`' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i += 2
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			t.dynamic = true
			i = psSkipParen(s, i+1)
		case c == '$' && i+1 < len(s) && psVarStart(s[i+1]):
			first := b.Len() == 0 && !t.quoted && !t.dynamic
			name, n := psVarName(s[i+1:])
			b.WriteString("\x00" + name + "\x00")
			i += 1 + n
			if j := psMembers(s, i); first && j > i {
				// $x.bak, $env:X.Length, $x[0]: an argument that starts with a
				// variable and a member or an index is an expression, not
				// "<x>.bak", and it ends after its members (#181).
				b.WriteString(s[i:j])
				t.text, t.dynamic = b.String(), true
				return t, j
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	t.text = b.String()
	return t, i
}

// psMembers is the end of the members and indexes after a variable at i, as
// PowerShell reads them in an argument: .name, .$name, ."name", .name(…),
// [i] and ::name, one after another; i when there is none.
func psMembers(s string, i int) int {
	ident := func(j int) int {
		for j < len(s) && (letter(s[j]) || s[j] == '_' || ('0' <= s[j] && s[j] <= '9')) {
			j++
		}
		return j
	}
	start, member := i, false
	for i < len(s) {
		c := s[i]
		var next byte
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch {
		case c == '.' && (letter(next) || next == '_' || ('0' <= next && next <= '9')):
			i, member = ident(i+1), true
		case c == '.' && next == '$' && i+2 < len(s) && psVarStart(s[i+2]):
			_, n := psVarName(s[i+2:])
			i, member = i+2+n, true
		case c == '.' && (next == '\'' || next == '"'):
			j := strings.IndexByte(s[i+2:], next)
			if j < 0 {
				return len(s)
			}
			i, member = i+3+j, true
		case c == ':' && next == ':' && i+2 < len(s) && (letter(s[i+2]) || s[i+2] == '_'):
			i, member = ident(i+2), true
		case c == '[':
			depth := 0
			for ; i < len(s); i++ {
				if s[i] == '[' {
					depth++
				} else if s[i] == ']' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if i >= len(s) {
				return len(s)
			}
			i, member = i+1, false
		case c == '(' && member && i > start:
			i, member = psSkipParen(s, i), false
		default:
			return i
		}
	}
	return i
}

// psDash is the length of the dash s starts with: 1 for -, 3 for the en
// dash, em dash and horizontal bar (U+2013 to U+2015) in UTF-8, which
// PowerShell reads as -; else 0.
func psDash(s string) int {
	switch {
	case strings.HasPrefix(s, "-"):
		return 1
	case strings.HasPrefix(s, "\u2013"), strings.HasPrefix(s, "\u2014"), strings.HasPrefix(s, "\u2015"):
		return 3
	}
	return 0
}

// psEscape is what `c means inside double quotes.
func psEscape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case '0':
		return 0
	}
	return c
}

// psSkipParen skips a $( ... ) from its ( at i, nested, and returns the index
// after it.
func psSkipParen(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

func psVarStart(c byte) bool { return letter(c) || c == '_' || c == '{' }

// psVarName reads a variable's name: name, env:NAME, or ${any}.
func psVarName(s string) (string, int) {
	if s[0] == '{' {
		if j := strings.IndexByte(s, '}'); j > 0 {
			return s[1:j], j + 1
		}
		return s[1:], len(s)
	}
	i := 0
	for i < len(s) && (letter(s[i]) || s[i] == '_' || ('0' <= s[i] && s[i] <= '9') || s[i] == ':') {
		i++
	}
	return strings.TrimSuffix(s[:i], ":"), i
}

// psReader reads statements, keeping the directory, its stack and the
// variables.
type psReader struct {
	res   *results
	vars  map[string]string
	set   map[string]bool // $env: and automatic variables the command sets: unknown; "env:\x00" for every $env:
	dir   string
	dirOK bool
	loc   string  // the drive the command moved to when it is no disk: "env", "variable", "hklm" …; "" for files
	stack []psLoc // Push-Location's
	depth int     // nested shells read
}

// psLoc is a location Push-Location saved.
type psLoc struct {
	dir, loc string
	ok       bool
}

// run reads a command's statements.
func (p *psReader) run(command string) {
	l := &psLexer{s: command}
	for _, st := range l.statements(false) {
		p.statement(st)
	}
}

// value expands the variables in a word. ok is false when it holds one
// stele does not know, or what it cannot know.
func (p *psReader) value(t psTok) (string, bool) {
	if t.dynamic || t.kind != psWordK {
		return "", false
	}
	if !strings.Contains(t.text, "\x00") {
		return t.text, true
	}
	// The length first: the values are counted against the budget before
	// the string is built, so many copies of a long variable are not (#171).
	// A token read twice, as a path and as a nested bash or wsl script, is
	// counted twice: that errs toward unknown.
	parts := strings.Split(t.text, "\x00")
	n, grown := 0, 0
	for i, part := range parts {
		if i%2 == 0 {
			n += len(part)
			continue
		}
		v, ok := p.lookup(part)
		if !ok {
			return "", false
		}
		if grown <= maxExpand {
			grown += len(v)
		}
	}
	if !p.res.expand(grown) {
		return "", false
	}
	var b strings.Builder
	b.Grow(n + grown)
	for i, part := range parts {
		if i%2 == 0 {
			b.WriteString(part)
		} else {
			v, _ := p.lookup(part) // the environment's value is cached
			b.WriteString(v)
		}
	}
	return b.String(), true
}

// lookup is the value of the variable named name, as written: one set to a
// literal earlier in the command, $env:NAME from the environment, $HOME, the
// user's home, or $PWD, the directory the command has moved to. ok is false
// for any other, one the command set otherwise, and an empty one, which
// stele cannot tell from an unset one. The environment is read once a name
// (results.env), and not at all once the budget is spent.
func (p *psReader) lookup(name string) (string, bool) {
	lower := strings.ToLower(name)
	if p.set[lower] || (p.set[allEnv] && strings.HasPrefix(lower, "env:")) {
		return "", false
	}
	if v, ok := p.vars[lower]; ok {
		return v, true
	}
	if p.res.grown > maxExpand {
		return "", false
	}
	var v string
	switch {
	case strings.HasPrefix(lower, "env:"):
		// Windows' environment ignores case; elsewhere $env:Path is not $env:PATH.
		env := name[len("env:"):]
		if windows {
			env = strings.ToUpper(env)
		}
		if env != "" {
			v = p.res.env(env)
		}
	case lower == "home":
		v = p.home()
	case lower == "pwd":
		if p.dirOK {
			v = p.dir
		}
	}
	return v, v != ""
}

// home is PowerShell's $HOME and ~: the user's profile folder on Windows,
// HOME elsewhere, as os.UserHomeDir reads them.
func (p *psReader) home() string {
	if windows {
		return p.res.env("USERPROFILE")
	}
	return p.res.env("HOME")
}

// unset marks a variable the command sets in a way stele does not follow.
func (p *psReader) unset(name string) {
	if p.set == nil {
		p.set = map[string]bool{}
	}
	p.set[name] = true
}

// allEnv in psReader.set stands for every $env: variable: the command set
// one stele cannot name.
const allEnv = "env:\x00"

// setItem marks the item of a drive that holds variables as set by the
// command: env:X, variable:x. An item stele cannot name, a wildcard, or the
// drive itself, marks them all.
func (p *psReader) setItem(drive, item string) {
	item = strings.ToLower(strings.TrimLeft(item, `/\`))
	all := item == "" || strings.ContainsAny(item, "*?[")
	switch drive {
	case "env":
		if all {
			p.unset(allEnv)
		} else {
			p.unset("env:" + item)
		}
	case "variable":
		if all {
			p.forgetAll()
			return
		}
		delete(p.vars, item)
		if automatic(item) {
			p.unset(item)
		}
	}
}

// forgetAll forgets every variable but $env:, as when Remove-Variable * runs.
func (p *psReader) forgetAll() {
	clear(p.vars)
	p.unset("home")
	p.unset("pwd")
}

// psProvider splits a path on a drive or provider that is no disk: env:X,
// Variable:\x, HKLM:\…, Microsoft.PowerShell.Core\Environment::X. drive is
// its name in lower case, env and variable for those two whatever they are
// called; item is what follows. ok is false for a file system path: C:\x,
// FileSystem::C:\x, a relative one.
func psProvider(v string) (prefix, drive, item string, ok bool) {
	if i := strings.Index(v, "::"); i >= 0 {
		name := strings.ToLower(v[:i])
		if j := strings.LastIndexAny(name, `\/`); j >= 0 {
			name = name[j+1:]
		}
		switch name {
		case "filesystem":
			return psProvider(v[i+2:])
		case "environment":
			name = "env"
		}
		return v[:i+2], name, v[i+2:], true
	}
	if j := strings.IndexByte(v, ':'); j > 1 && !strings.ContainsAny(v[:j], `/\`) {
		return v[:j+1], strings.ToLower(v[:j]), v[j+1:], true
	}
	return "", "", "", false
}

// markItems marks every env: and variable: item a statement names as set,
// whatever the command: Set-Item, si, Clear-Item, Rename-Item, New-Item and
// the rest write them in ways stele does not follow one by one (#181). Its
// arguments, -Path:x included, and the elements of an array are read.
func (p *psReader) markItems(args []psTok) {
	for _, t := range args {
		if t.kind == psGroupK {
			if t.array {
				p.markItems(groupArgs(t))
			}
			continue
		}
		if t.kind != psWordK {
			continue
		}
		if t.param { // -Path:env:X
			_, rest, has := strings.Cut(t.text, ":")
			if !has {
				continue
			}
			t.text, t.param = rest, false
		}
		if !p.mayName(t) {
			continue
		}
		v, ok := p.peek(t)
		if !ok {
			// env:$x, env:$(…): an item of the drive stele cannot name.
			lit := t.text
			if j := strings.IndexByte(lit, 0); j >= 0 {
				lit = lit[:j]
			}
			if _, drive, _, ok := psProvider(lit); ok {
				p.setItem(drive, "")
			}
			continue
		}
		if _, drive, item, ok := psProvider(v); ok {
			p.setItem(drive, item)
		}
	}
}

// maxItem is the longest word markItems reads as an item: a variable's name
// is short, and a longer word is not expanded twice.
const maxItem = 4 << 10

// peek is value for markItems: a word up to maxItem bytes, not counted
// against the budget, which reading it as a path counts.
func (p *psReader) peek(t psTok) (string, bool) {
	if t.dynamic || len(t.text) > maxItem {
		return "", false
	}
	parts := strings.Split(t.text, "\x00")
	n := 0
	for i, part := range parts {
		if i%2 == 0 {
			n += len(part)
			continue
		}
		v, ok := p.lookup(part)
		if !ok {
			return "", false
		}
		if n += len(v); n > maxItem {
			return "", false
		}
	}
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 0 {
			b.WriteString(part)
		} else {
			v, _ := p.lookup(part)
			b.WriteString(v)
		}
	}
	return b.String(), true
}

// mayName reports whether a word may name a drive: a colon written in it,
// or a variable set to a literal, which may hold one. Others are not
// expanded twice.
func (p *psReader) mayName(t psTok) bool {
	s := t.text
	for i := 0; ; i++ {
		j := strings.IndexByte(s, 0)
		part := s
		if j >= 0 {
			part = s[:j]
		}
		if i%2 == 0 {
			if strings.IndexByte(part, ':') >= 0 {
				return true
			}
		} else if _, ok := p.vars[strings.ToLower(part)]; ok {
			return true
		}
		if j < 0 {
			return false
		}
		s = s[j+1:]
	}
}

// automatic reports whether name, lower case, is a variable whose value
// stele reads from outside the command: $env:NAME, $HOME, $PWD.
func automatic(name string) bool {
	return strings.HasPrefix(name, "env:") || name == "home" || name == "pwd"
}

// varName returns the name of a token that is one variable.
func varName(t psTok) (string, bool) {
	w := t.text
	if t.kind != psWordK || t.quoted || len(w) < 3 || w[0] != 0 || w[len(w)-1] != 0 || strings.Count(w, "\x00") != 2 {
		return "", false
	}
	return strings.ToLower(w[1 : len(w)-1]), true
}

// psAssign are the assignment operators.
var psAssign = map[string]bool{"=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "??=": true}

// psCommands are the cmdlets, by every name and alias, that touch files.
var psCommands = map[string]string{
	"set-content": "set", "sc": "set",
	"add-content": "set", "ac": "set",
	"out-file":   "out",
	"tee-object": "tee", "tee": "tee",
	"export-csv": "out", "epcsv": "out", "export-clixml": "out", "start-transcript": "out",
	"new-item": "new", "ni": "new", "mkdir": "mkdir", "md": "mkdir",
	"clear-content": "clear", "clc": "clear",
	"copy-item": "copy", "cp": "copy", "copy": "copy", "cpi": "copy",
	"move-item": "move", "mv": "move", "move": "move", "mi": "move",
	"rename-item": "rename", "ren": "rename", "rni": "rename",
	"remove-item": "remove", "rm": "remove", "del": "remove", "ri": "remove", "erase": "remove", "rd": "remove", "rmdir": "remove",
	"set-location": "cd", "cd": "cd", "sl": "cd", "chdir": "cd",
	"push-location": "pushd", "pushd": "pushd",
	"pop-location": "popd", "popd": "popd",
	"invoke-webrequest": "web", "iwr": "web", "curl": "web", "wget": "web", "invoke-restmethod": "web", "irm": "web",
	"new-object": "object",
	"powershell": "pwsh", "powershell.exe": "pwsh", "pwsh": "pwsh", "pwsh.exe": "pwsh",
	"cmd": "cmd", "cmd.exe": "cmd",
	"bash": "bash", "bash.exe": "bash", "sh": "bash", "zsh": "bash", "dash": "bash", "ksh": "bash", "wsl": "wsl", "wsl.exe": "wsl",
	"invoke-expression": "iex", "iex": "iex", "start-process": "opaque", "saps": "opaque", "start": "opaque",
	"invoke-command": "opaque", "icm": "opaque",
	"git": "git", "git.exe": "git",
	"set-variable": "var", "sv": "var", "new-variable": "var", "nv": "var",
	"clear-variable": "var", "clv": "var", "remove-variable": "var", "rv": "var",
}

// psReadOnly are common commands that read env: and variable: without
// setting them, as in Get-ChildItem env:.
var psReadOnly = map[string]bool{"get-childitem": true, "gci": true, "dir": true, "ls": true,
	"get-item": true, "gi": true, "get-itemproperty": true, "gp": true, "get-content": true, "gc": true,
	"cat": true, "type": true, "test-path": true, "resolve-path": true, "rvpa": true,
	"write-output": true, "echo": true, "write": true, "write-host": true}

// psCommon are the parameters every cmdlet takes a value for.
var psCommon = []string{"erroraction", "ea", "warningaction", "wa", "errorvariable", "ev", "warningvariable", "wv",
	"outvariable", "ov", "outbuffer", "ob", "pipelinevariable", "pv", "informationaction", "infa", "informationvariable", "iv"}

// psSwitches are the switches stele knows: any other parameter it does not
// know is taken to take a value, and counted as unknown.
var psSwitches = map[string]bool{"force": true, "recurse": true, "nonewline": true, "append": true, "passthru": true,
	"whatif": true, "confirm": true, "verbose": true, "debug": true, "noclobber": true, "raw": true, "asbytestream": true,
	"container": true, "notypeinformation": true, "usebasicparsing": true, "includeheader": true, "noprofile": true,
	"noninteractive": true, "nologo": true}

// psParams are, per kind of command, its parameters that take a value.
var psParams = map[string][]string{
	"set":    {"path", "literalpath", "value", "encoding", "stream", "filter", "include", "exclude", "credential", "itemtype", "delimiter"},
	"out":    {"filepath", "path", "literalpath", "inputobject", "encoding", "width", "delimiter", "depth", "outputdirectory"},
	"tee":    {"filepath", "literalpath", "inputobject", "variable", "encoding"},
	"new":    {"path", "name", "itemtype", "type", "value", "credential", "target"},
	"mkdir":  {"path", "name", "value", "credential"},
	"clear":  {"path", "literalpath", "filter", "include", "exclude", "stream", "credential"},
	"copy":   {"path", "literalpath", "destination", "filter", "include", "exclude", "credential", "tosession", "fromsession"},
	"move":   {"path", "literalpath", "destination", "filter", "include", "exclude", "credential"},
	"rename": {"path", "literalpath", "newname", "credential"},
	"remove": {"path", "literalpath", "filter", "include", "exclude", "stream", "credential"},
	"cd":     {"path", "literalpath", "stackname"},
	"pushd":  {"path", "literalpath", "stackname"},
	"popd":   {"stackname"},
	"web":    {"uri", "outfile", "method", "body", "headers", "contenttype", "infile", "credential", "useragent", "timeoutsec"},
	"object": {"typename", "argumentlist", "comobject", "property"},
	"pwsh":   {"command", "c", "file", "f", "executionpolicy", "ep", "encodedcommand", "e", "ec", "workingdirectory"},
	"iex":    {"command"},
	"var":    {"name", "value", "scope", "option", "visibility", "description", "include", "exclude"},
}

// psParam resolves a parameter as written, -Pa or -Path:x, to its full name
// among names: PowerShell takes any unique prefix. ok is false for none, or
// more than one.
func psParam(word string, names []string) (name, inline string, hasInline, ok bool) {
	word = strings.TrimPrefix(word, "-")
	word, inline, hasInline = strings.Cut(word, ":")
	word = strings.ToLower(word)
	var found []string
	for _, n := range names {
		if n == word {
			return n, inline, hasInline, true
		}
		if strings.HasPrefix(n, word) {
			found = append(found, n)
		}
	}
	if len(found) == 1 {
		return found[0], inline, hasInline, true
	}
	return word, inline, hasInline, false
}

// statement reads one statement.
func (p *psReader) statement(st psStmt) {
	// Redirections first: they write whatever the command is.
	var words []psTok
	for i := 0; i < len(st.toks); i++ {
		t := st.toks[i]
		switch {
		case t.kind == psMergeK:
		case t.kind == psRedirK:
			if i+1 < len(st.toks) {
				i++
				p.addTok(st.toks[i], Redirect, false)
			}
		default:
			words = append(words, t)
		}
	}
	// A cast, [void](…), and a parenthesised command are read as the
	// command inside.
	for len(words) > 0 && isCast(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return
	}
	if words[0].kind == psGroupK {
		p.groups(words[0], st.depth)
		for _, w := range words[1:] {
			if w.kind == psGroupK {
				p.groups(w, st.depth)
			}
		}
		return
	}
	// An assignment: a literal sets the variable at the top level; anything
	// else leaves it unknown, and a command on its right is read.
	if len(words) >= 2 && words[1].kind == psWordK && psAssign[words[1].text] {
		if name, ok := varName(words[0]); ok {
			rhs := words[2:]
			delete(p.vars, name)
			if automatic(name) {
				// $env:X = …, or $HOME, which PowerShell refuses: unknown
				// from then on, rather than read from outside.
				p.unset(name)
			}
			if v, ok := p.value(firstOr(rhs)); ok && words[1].text == "=" && st.depth == 0 && len(rhs) == 1 && rhs[0].kind == psWordK && !strings.Contains(name, ":") {
				if _, cmd := psCommands[strings.ToLower(v)]; !cmd || rhs[0].quoted {
					p.vars[name] = v
					return
				}
			}
			p.statement(psStmt{toks: rhs, depth: st.depth, piped: st.piped})
			return
		}
	}
	// & cmd, . cmd: the command is the next word.
	if (words[0].text == "&" || words[0].text == ".") && !words[0].quoted && len(words) > 1 {
		words = words[1:]
	}
	head := words[0]
	if head.kind != psWordK || head.dynamic {
		return
	}
	name := head.text
	if strings.EqualFold(name, "foreach") && !head.quoted && len(words) > 1 && words[1].kind == psGroupK {
		// foreach ($x in …) sets $x, $env:X too, to each item (#181).
		if g := words[1].group; len(g) > 0 && len(g[0].toks) > 1 && strings.EqualFold(g[0].toks[1].text, "in") {
			if v, ok := varName(g[0].toks[0]); ok {
				delete(p.vars, v)
				if automatic(v) {
					p.unset(v)
				}
			}
		}
		return
	}
	if strings.HasPrefix(name, "[") && strings.Contains(name, "]::") {
		p.dotnet(name, words[1:])
		return
	}
	// Module-qualified: Microsoft.PowerShell.Management\Remove-Item.
	if i := strings.LastIndexAny(name, `\/`); i >= 0 && !strings.ContainsAny(name[:i], ":") && strings.Contains(name[:i], ".") {
		name = name[i+1:]
	}
	kind, ok := psCommands[strings.ToLower(name)]
	if !psReadOnly[strings.ToLower(name)] && kind != "cd" && kind != "pushd" && kind != "popd" {
		p.markItems(words[1:])
	}
	if !ok {
		// A program run by its path, such as C:\Windows\System32\cmd.exe.
		if k, ok2 := psCommands[strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))]; ok2 && (k == "pwsh" || k == "cmd" || k == "bash" || k == "wsl" || k == "git") {
			kind, ok = k, true
		}
	}
	if !ok {
		return
	}
	p.cmdlet(kind, words[1:], st)
}

func firstOr(ts []psTok) psTok {
	if len(ts) > 0 {
		return ts[0]
	}
	return psTok{dynamic: true}
}

// isCast reports whether t is a type in brackets, [void], before a value.
func isCast(t psTok) bool {
	return t.kind == psWordK && !t.quoted && strings.HasPrefix(t.text, "[") && strings.HasSuffix(t.text, "]") && !strings.Contains(t.text, "::")
}

// groups reads the commands inside a group.
func (p *psReader) groups(g psTok, depth int) {
	for _, st := range g.group {
		st.depth += depth
		p.statement(st)
	}
}

// cmdlet reads a cmdlet's arguments: named ones, then positional.
func (p *psReader) cmdlet(kind string, args []psTok, st psStmt) {
	if (kind == "iex" || kind == "pwsh" || kind == "bash" || kind == "wsl" || kind == "cmd") && p.res.nested > maxNested {
		p.res.unknown++ // past the budget of nested scripts: not read
		return
	}
	if kind == "bash" || kind == "wsl" {
		// bash -c '…', sh -lc '…', wsl -e sh -c '…': read the script as bash.
		ws := make([]word, len(args))
		for i, a := range args {
			v, ok := p.value(a)
			ws[i] = word{text: v, dynamic: !ok}
		}
		if kind == "wsl" {
			// wsl's own options, then the shell it runs and its arguments.
			n := wslShell(ws)
			if n < 0 {
				p.res.unknown++
				return
			}
			args, ws = args[n:], ws[n:]
		}
		if i, ok := shellScript(ws); ok {
			p.nested(args[i:i+1], true)
			return
		}
		p.res.unknown++
		return
	}
	if kind == "git" {
		// git init, clone, worktree add and submodule add, read as in bash.
		ws := make([]word, len(args))
		for i, a := range args {
			v, ok := p.value(a)
			ws[i] = word{text: v, dynamic: !ok}
		}
		made, unknown := gitMakes(ws)
		p.res.unknown += unknown
		for _, w := range made {
			p.addTok(psTok{text: w.text, quoted: true}, Create, true)
		}
		return
	}
	names := append(append([]string{}, psParams[kind]...), psCommon...)
	named := map[string][]psTok{}
	var pos [][]psTok
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.kind == psGroupK && !a.array {
			p.groups(a, st.depth) // a command in parentheses runs
		}
		if a.param && a.kind == psWordK {
			name, inline, hasInline, known := psParam(a.text, names)
			if !known && psSwitches[name] && !hasInline {
				continue
			}
			var vals []psTok
			switch {
			case hasInline:
				v := a
				v.text, v.param = inline, false
				vals = append(vals, v)
				for v.comma && i+1 < len(args) {
					i++
					v = args[i]
					vals = append(vals, v)
				}
			case i+1 < len(args) && !args[i+1].param:
				for i+1 < len(args) {
					i++
					vals = append(vals, args[i])
					if !args[i].comma {
						break
					}
				}
			}
			if !known {
				p.res.unknown++ // a parameter stele does not know: its value is no path stele can place
				continue
			}
			named[name] = append(named[name], vals...)
			continue
		}
		group := []psTok{a}
		for a.comma && i+1 < len(args) {
			i++
			a = args[i]
			group = append(group, a)
		}
		pos = append(pos, group)
	}
	at := func(n int) []psTok {
		if n < len(pos) {
			return pos[n]
		}
		return nil
	}
	path := func(first int) ([]psTok, bool) {
		if v := named["literalpath"]; v != nil {
			return v, true
		}
		if v := named["path"]; v != nil {
			return v, false
		}
		if v := named["filepath"]; v != nil {
			return v, false
		}
		return at(first), false
	}
	// A cmdlet that writes with no path takes it from the pipeline: unknown.
	needPath := func(ps []psTok) bool {
		if len(ps) == 0 {
			p.res.unknown++
			return false
		}
		return true
	}
	itemType := ""
	if v := append(named["itemtype"], named["type"]...); len(v) > 0 {
		itemType, _ = p.value(v[0])
		itemType = strings.ToLower(itemType)
	}
	switch kind {
	case "set", "out":
		if ps, lit := path(0); needPath(ps) {
			p.addAll(ps, Redirect, lit)
		}
	case "tee":
		if named["variable"] != nil {
			return
		}
		if ps, lit := path(0); needPath(ps) {
			p.addAll(ps, Redirect, lit)
		}
	case "new", "mkdir":
		k := Redirect
		switch {
		case kind == "mkdir" || strings.HasPrefix("directory", itemType) && itemType != "":
			k = Create // a folder: no content, but a .git or one of stele's own names matters
		case itemType == "" || strings.HasPrefix("file", itemType):
		case strings.HasPrefix("symboliclink", itemType), strings.HasPrefix("hardlink", itemType), strings.HasPrefix("junction", itemType):
			k = Copy // a link at the path: what is there now
		default:
			return // a registry key or the like: no file
		}
		ps, lit := path(0)
		if name := named["name"]; name != nil {
			if ps == nil {
				p.addAll(name, k, true)
				return
			}
			for _, d := range ps {
				dv, ok := p.value(d)
				for _, n := range name {
					nv, nok := p.value(n)
					if !ok || !nok {
						p.res.unknown++
						continue
					}
					p.addTok(psTok{text: filepath.Join(psPath(dv), psPath(nv)), quoted: true}, k, true)
				}
			}
			return
		}
		if needPath(ps) {
			p.addAll(ps, k, lit)
		}
	case "clear":
		if ps, lit := path(0); needPath(ps) {
			p.addAll(ps, Copy, lit)
		}
	case "remove":
		if ps, lit := path(0); needPath(ps) {
			p.addAll(ps, Remove, lit)
		}
	case "copy", "move":
		ps, lit := path(0)
		if !needPath(ps) {
			return
		}
		if kind == "move" {
			p.addAll(ps, Remove, lit)
		}
		dest := p.second(named, "destination", at)
		if len(dest) == 0 {
			// Into the current directory, each under its own name.
			for _, s := range ps {
				if v, ok := p.value(s); ok {
					p.addTok(psTok{text: filepath.Base(psPath(v)), quoted: true}, Copy, true)
				} else {
					p.res.unknown++
				}
			}
			return
		}
		provSrc := false // Copy-Item env:A B: B is no file stele can place
		for _, s := range ps {
			if v, ok := p.value(s); ok {
				_, _, _, prov := psProvider(v)
				provSrc = provSrc || prov
			}
		}
		for _, d := range dest {
			dv, ok := p.value(d)
			if _, _, _, prov := psProvider(dv); provSrc && ok && !prov && d.kind == psWordK {
				p.res.unknown++
				continue
			}
			dpath, dok := p.abs(dv)
			if ok && dok && isDir(dpath) {
				// Into a directory that is there: each under its own name.
				for _, s := range ps {
					if v, ok := p.value(s); ok {
						p.res.add(filepath.Join(dpath, filepath.Base(psPath(v))), Copy)
					} else {
						p.res.unknown++
					}
				}
				continue
			}
			p.addTok(d, Copy, true)
		}
	case "rename":
		ps, lit := path(0)
		if !needPath(ps) {
			return
		}
		p.addAll(ps, Remove, lit)
		nn := p.second(named, "newname", at)
		for _, src := range ps {
			sv, sok := p.value(src)
			for _, n := range nn {
				nv, ok := p.value(n)
				if !ok || !sok {
					p.res.unknown++
					continue
				}
				if _, drive, _, prov := psProvider(sv); prov || (p.loc != "" && !filepath.IsAbs(psPath(sv))) {
					// Rename-Item env:FOO BAR sets env:BAR: no file (#181).
					if !prov {
						drive = p.loc
					}
					p.setItem(drive, nv)
					continue
				}
				// The new name is in the item's directory, unless it is a path.
				if !strings.ContainsAny(nv, `/\`) {
					nv = filepath.Join(filepath.Dir(psPath(sv)), psPath(nv))
				}
				p.addTok(psTok{text: nv, quoted: true}, Copy, true)
			}
		}
	case "cd", "pushd":
		ps, _ := path(0)
		if kind == "pushd" {
			p.stack = append(p.stack, psLoc{p.dir, p.loc, p.dirOK})
		}
		if len(ps) == 0 {
			return
		}
		v, ok := p.value(ps[0])
		if !ok {
			p.dirOK = false
			return
		}
		if _, drive, _, ok := psProvider(v); ok {
			// cd env:, HKLM:\…: a drive that is no disk. A relative path
			// names its items from then on, not files (#181).
			p.loc, p.dirOK = drive, false
			return
		}
		if strings.Contains(v, "::") { // FileSystem::C:\x
			v = v[strings.Index(v, "::")+2:]
		}
		if d, ok := p.abs(v); ok {
			p.dir, p.loc = d, ""
		} else {
			p.dirOK = false
		}
	case "popd":
		if n := len(p.stack); n > 0 {
			l := p.stack[n-1]
			p.dir, p.loc, p.dirOK, p.stack = l.dir, l.loc, l.ok, p.stack[:n-1]
		} else {
			p.dirOK = false
		}
	case "web":
		if out := named["outfile"]; out != nil {
			p.addAll(out, Redirect, true)
		}
	case "object":
		typ := named["typename"]
		if typ == nil {
			typ = at(0)
		}
		if len(typ) == 0 || !streamType(typ[0].text) {
			return
		}
		argl := named["argumentlist"]
		if argl == nil {
			argl = at(1)
			if named["typename"] != nil {
				argl = at(0)
			}
		}
		if len(argl) > 0 && argl[0].kind == psGroupK {
			argl = groupArgs(argl[0])
		}
		if len(argl) > 0 {
			p.addTok(argl[0], Redirect, true)
		}
	case "pwsh":
		script := named["command"]
		if script == nil {
			script = named["c"]
		}
		if named["file"] != nil || named["f"] != nil {
			p.res.unknown++
			return
		}
		if enc := append(append(named["encodedcommand"], named["e"]...), named["ec"]...); enc != nil {
			v, ok := p.value(enc[0])
			if script, ok2 := decodeCommand(v); ok && ok2 {
				p.nestedText(script, false)
			} else {
				p.res.unknown++
			}
			return
		}
		if script == nil {
			script = at(0)
		}
		p.nested(script, false)
	case "cmd":
		// cmd /c "…": its redirections and commands read as bash's. cmd's
		// own %VAR%, and !VAR! after /v:on, expand to what stele does not
		// read: such a script is unknown, not a literal path (#181).
		delayed := false
		for i, g := range pos {
			if len(g) > 0 {
				if o := strings.ToLower(g[0].text); o == "/v" || strings.HasPrefix(o, "/v:on") {
					delayed = true
				}
			}
			if len(g) > 0 && (strings.EqualFold(g[0].text, "/c") || strings.EqualFold(g[0].text, "/k")) {
				var rest []string
				for _, h := range pos[i+1:] {
					for _, w := range h {
						if v, ok := p.value(w); ok {
							rest = append(rest, v)
						} else {
							p.res.unknown++
							return
						}
					}
				}
				script := strings.Join(rest, " ")
				if cmdExpands(script, '%') || (delayed && cmdExpands(script, '!')) {
					p.res.unknown++
					return
				}
				p.nestedText(script, true)
				return
			}
		}
		p.res.unknown++
	case "iex":
		// Invoke-Expression 'literal': its statements run here, as a group's.
		script := named["command"]
		if script == nil {
			script = at(0)
		}
		v, ok := "", false
		if len(script) == 1 {
			v, ok = p.value(script[0])
		}
		if !ok || p.depth >= maxDepth || !p.res.reread(len(v)) {
			p.res.unknown++ // from the pipeline, or what stele cannot know
			return
		}
		p.depth++
		l := &psLexer{s: v}
		for _, inner := range l.statements(false) {
			if p.res.nested > maxNested {
				p.res.unknown++ // past the budget: the rest is not read
				break
			}
			inner.depth += st.depth
			p.statement(inner)
		}
		p.depth--
	case "opaque":
		p.res.unknown++ // Start-Process, Invoke-Command: stele cannot see what runs
	case "var":
		// Set-Variable, New-Variable, Clear-Variable, Remove-Variable: the
		// variables they name are unknown from then on (#181). A name stele
		// cannot read, a wildcard, -Include or -Exclude, or names from the
		// pipeline: every variable is.
		ns := named["name"]
		if ns == nil {
			ns = at(0)
		}
		if len(ns) == 0 || named["include"] != nil || named["exclude"] != nil {
			p.forgetAll()
			return
		}
		for _, n := range ns {
			v, ok := "", false
			if n.kind == psWordK {
				v, ok = p.value(n)
			}
			if !ok {
				p.forgetAll()
				return
			}
			// A scope before the name, global:x, is no part of it.
			if s, rest, has := strings.Cut(v, ":"); has && psScope[strings.ToLower(s)] {
				v = rest
			}
			p.setItem("variable", v)
		}
	}
}

// psScope are the scopes a variable's name may start with.
var psScope = map[string]bool{"global": true, "local": true, "script": true, "private": true}

// cmdExpands reports whether a cmd script holds a variable between two of
// mark: %NAME%, !NAME!.
func cmdExpands(script string, mark byte) bool {
	i := strings.IndexByte(script, mark)
	return i >= 0 && strings.IndexByte(script[i+1:], mark) > 0
}

// decodeCommand decodes the script of -EncodedCommand: base64 of UTF-16LE.
func decodeCommand(s string) (string, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%2 != 0 {
		return "", false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	// A byte order mark before the script is no part of it.
	return strings.TrimPrefix(string(utf16.Decode(u)), "\uFEFF"), true
}

// wslOptions are the options of wsl that take a value.
var wslOptions = map[string]bool{"-d": true, "--distribution": true, "-u": true, "--user": true, "--cd": true, "--shell-type": true}

// wslShell skips wsl's options, up to -e, --exec or --, and returns the index
// of the arguments after the shell wsl runs, or -1 when that is no shell
// whose -c stele reads.
func wslShell(ws []word) int {
	i := 0
	for i < len(ws) && !ws[i].dynamic && strings.HasPrefix(ws[i].text, "-") {
		t := ws[i].text
		i++
		if t == "-e" || t == "--exec" || t == "--" {
			break
		}
		if wslOptions[t] {
			i++
		}
	}
	if i >= len(ws) || ws[i].dynamic {
		return -1
	}
	switch name(ws[i]) {
	case "bash", "sh", "zsh", "dash", "ksh":
		return i + 1
	}
	return -1
}

// nested reads a nested shell's script, given as a literal.
func (p *psReader) nested(script []psTok, bash bool) {
	if len(script) == 0 {
		p.res.unknown++
		return
	}
	v, ok := p.value(script[0])
	if !ok {
		p.res.unknown++
		return
	}
	p.nestedText(v, bash)
}

// nestedText reads a nested shell's script, in the directory as it is.
func (p *psReader) nestedText(script string, bash bool) {
	if p.depth >= maxDepth || !p.dirOK {
		p.res.unknown++
		return
	}
	if bash {
		// Its depth adds to this one's; a script it cannot read whole is
		// unknown. An environment variable this command set is unknown there,
		// in any case: bash's names are the same ones (#181).
		fold := map[string]bool{}
		for k := range p.set {
			if name, ok := strings.CutPrefix(k, "env:"); ok {
				fold[name] = true
			}
		}
		if !readBash(p.res, script, p.dir, p.dirOK, p.depth, map[string]bool{}, fold) {
			p.res.unknown++
		}
		return
	}
	if !p.res.reread(len(script)) {
		p.res.unknown++
		return
	}
	if p.set == nil {
		p.set = map[string]bool{}
	}
	// The environment variables set so far, and any the script sets, stay
	// unknown on both sides.
	q := &psReader{res: p.res, vars: map[string]string{}, set: p.set, dir: p.dir, dirOK: p.dirOK, depth: p.depth + 1}
	q.run(script)
}

// streamType reports whether a type name is one whose constructor opens a
// file for writing.
func streamType(name string) bool {
	switch strings.ToLower(strings.Trim(name, "[]")) {
	case "io.streamwriter", "system.io.streamwriter", "io.filestream", "system.io.filestream":
		return true
	}
	return false
}

// groupArgs are the comma-separated arguments in a group, a method call's.
func groupArgs(g psTok) []psTok {
	if len(g.group) == 0 {
		return nil
	}
	var out []psTok
	for _, st := range g.group {
		out = append(out, st.toks...)
	}
	return out
}

// second is a command's second argument, a destination or a new name: the
// parameter name, or else the first positional one when the path was named,
// the second when it was not.
func (p *psReader) second(named map[string][]psTok, name string, at func(int) []psTok) []psTok {
	if v := named[name]; v != nil {
		return v
	}
	if named["path"] != nil || named["literalpath"] != nil {
		return at(0)
	}
	return at(1)
}

// dotnet reads a call of a .NET method: [IO.File]'s writes, copies, moves
// and deletes, their Async forms, and [IO.StreamWriter]::new(path). Its
// arguments are the group after the method.
func (p *psReader) dotnet(head string, rest []psTok) {
	typ, method, _ := strings.Cut(head, "]::")
	typ = strings.ToLower(strings.TrimPrefix(typ, "["))
	method = strings.TrimSuffix(strings.ToLower(method), "async")
	var args []psTok
	if len(rest) > 0 && rest[0].kind == psGroupK {
		args = groupArgs(rest[0])
	} else {
		args = rest
	}
	if (typ == "environment" || typ == "system.environment") && method == "setenvironmentvariable" {
		// [Environment]::SetEnvironmentVariable('X', …) sets $env:X.
		name := ""
		if len(args) > 0 {
			name, _ = p.value(args[0])
		}
		p.setItem("env", name) // a name stele cannot read: every one
		return
	}
	var kinds []Kind
	switch {
	case streamType(typ) && method == "new":
		kinds = []Kind{Redirect}
	case typ != "io.file" && typ != "system.io.file":
		return
	default:
		switch method {
		case "writealltext", "writealllines", "writeallbytes", "appendalltext", "appendalllines", "create", "createtext", "appendtext", "open", "openwrite":
			kinds = []Kind{Redirect}
		case "copy":
			kinds = []Kind{-1, Copy}
		case "move":
			kinds = []Kind{Remove, Copy}
		case "replace":
			kinds = []Kind{Remove, Redirect}
		case "delete":
			kinds = []Kind{Remove}
		default:
			return
		}
	}
	for i, k := range kinds {
		if i >= len(args) {
			p.res.unknown++
			return
		}
		if k >= 0 {
			p.addTok(args[i], k, true)
		}
	}
}

// addAll adds each path of an array.
func (p *psReader) addAll(ts []psTok, k Kind, literal bool) {
	for _, t := range ts {
		p.addTok(t, k, literal)
	}
}

// addTok adds the file a token names: a word, or each element of an array
// literal. Any other group is an expression: unknown.
func (p *psReader) addTok(t psTok, k Kind, literal bool) {
	if t.kind == psGroupK {
		if !t.array {
			p.res.unknown++
			return
		}
		for _, e := range groupArgs(t) {
			p.addTok(e, k, literal)
		}
		return
	}
	if strings.EqualFold(t.text, "\x00null\x00") {
		return // $null: the output goes nowhere
	}
	v, ok := p.value(t)
	if !ok {
		p.res.unknown++
		return
	}
	if v == "" {
		return
	}
	// A provider path: FileSystem::C:\x is the file; env:, variable:,
	// function: … are drives that are no disk. Writing env:X or variable:x
	// sets the variable: unknown from then on.
	if _, drive, item, ok := psProvider(v); ok {
		p.setItem(drive, item)
		return
	}
	if i := strings.Index(v, "::"); i >= 0 { // FileSystem::C:\x
		v = v[i+2:]
	}
	if device(v) {
		return
	}
	if p.loc != "" && !filepath.IsAbs(psPath(v)) {
		// After cd env: or the like, a relative path is an item there.
		p.setItem(p.loc, v)
		if p.loc != "env" && p.loc != "variable" {
			p.res.unknown++ // HKLM:\…: no file stele can place
		}
		return
	}
	path, ok := p.abs(v)
	if !ok {
		p.res.unknown++
		return
	}
	if !literal && strings.ContainsAny(v, "*?[") {
		p.glob(path, k)
		return
	}
	p.res.add(path, k)
}

// glob adds what pattern matches, within the command's glob budget.
func (p *psReader) glob(pattern string, k Kind) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		p.res.unknown++
		return
	}
	for _, m := range matches {
		if p.res.globbed >= maxGlob {
			p.res.unknown++
			return
		}
		p.res.globbed++
		p.res.add(filepath.Clean(m), k)
	}
}

// psPath is a PowerShell path in the OS's separators: PowerShell takes \ and
// / as separators on every OS, pwsh on Linux and macOS too.
func psPath(v string) string { return filepath.FromSlash(strings.ReplaceAll(v, `\`, "/")) }

// abs resolves a PowerShell path against the directory: absolute, ~ for the
// home directory, else relative. A path from the current drive's root, \x, is
// unknown, as is one with no directory to resolve against.
func (p *psReader) abs(v string) (string, bool) {
	v = psPath(v)
	if v == "~" || strings.HasPrefix(v, "~"+string(filepath.Separator)) {
		home := p.home()
		if home == "" || !p.res.expand(len(home)) {
			return "", false
		}
		return filepath.Join(home, v[1:]), true
	}
	if filepath.IsAbs(v) {
		return filepath.Clean(v), true
	}
	if strings.HasPrefix(v, string(filepath.Separator)) || (len(v) >= 2 && v[1] == ':') {
		return "", false
	}
	if !p.dirOK {
		return "", false
	}
	return filepath.Join(p.dir, v), true
}

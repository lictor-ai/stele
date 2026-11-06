package shell

import (
	"os"
	"path/filepath"
	"strings"
)

// PowerShellTargets returns the files a PowerShell command writes, resolved
// from cwd, in the order they appear, each once, as Targets does for bash
// (D77). Codex's shell runs PowerShell on Windows. It is best effort: what it
// cannot follow it counts as unknown, and stele check in CI is the catch.
//
// It reads redirections (> >> *> n>), after any word; Set-Content,
// Add-Content, Out-File, Tee-Object, New-Item (files and links),
// Clear-Content, Copy-Item, Move-Item, Rename-Item, Remove-Item, Export-Csv,
// Export-Clixml, Start-Transcript and Invoke-WebRequest -OutFile, by any
// alias or module-qualified name, with abbreviated and colon-joined
// parameters and the common ones; [IO.File]'s write, copy, move and delete
// methods, their Async forms, and an IO.StreamWriter made on a path. It
// follows Set-Location, Push-Location and Pop-Location, and variables set to
// a literal at the top level: one set in a block, from a command, or added
// to, is unknown from then on. A command assigned, cast, or in parentheses
// is read as a command. Here-strings, array literals and nested powershell,
// pwsh, bash and cmd commands with a literal script are read. Expressions as
// paths, splatting, $(...), $env:, unknown variables, paths bound from the
// pipeline, Invoke-Expression, Start-Process and scripts are unknown or not
// seen.
func PowerShellTargets(command, cwd string) (ts []Target, unknown int) {
	if len(command) > maxCommand {
		return nil, 1
	}
	r := &results{seen: map[string]int{}}
	p := &psReader{res: r, vars: map[string]string{}, dir: cwd, dirOK: cwd != ""}
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
	if s[0] == '-' && len(s) > 1 && letter(s[1]) {
		t.param = true
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
			name, n := psVarName(s[i+1:])
			b.WriteString("\x00" + name + "\x00")
			i += 1 + n
		default:
			b.WriteByte(c)
			i++
		}
	}
	t.text = b.String()
	return t, i
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
	dir   string
	dirOK bool
	stack []string // Push-Location's
	depth int      // nested shells read
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
	parts := strings.Split(t.text, "\x00")
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 0 {
			b.WriteString(part)
			continue
		}
		v, ok := p.vars[strings.ToLower(part)]
		if !ok {
			return "", false
		}
		b.WriteString(v)
	}
	return b.String(), true
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
	"new-item": "new", "ni": "new",
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
	"bash": "bash", "bash.exe": "bash", "sh": "bash", "wsl": "bash", "wsl.exe": "bash",
	"invoke-expression": "opaque", "iex": "opaque", "start-process": "opaque", "saps": "opaque", "start": "opaque",
	"invoke-command": "opaque", "icm": "opaque",
}

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
	"pwsh":   {"command", "c", "file", "f", "executionpolicy", "ep", "encodedcommand", "e", "workingdirectory"},
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
	if strings.HasPrefix(name, "[") && strings.Contains(name, "]::") {
		p.dotnet(name, words[1:])
		return
	}
	// Module-qualified: Microsoft.PowerShell.Management\Remove-Item.
	if i := strings.LastIndexAny(name, `\/`); i >= 0 && !strings.ContainsAny(name[:i], ":") && strings.Contains(name[:i], ".") {
		name = name[i+1:]
	}
	kind, ok := psCommands[strings.ToLower(name)]
	if !ok {
		// A program run by its path, such as C:\Windows\System32\cmd.exe.
		if k, ok2 := psCommands[strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))]; ok2 && (k == "pwsh" || k == "cmd" || k == "bash") {
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
	if kind == "bash" {
		// bash -c '…', sh -c '…', wsl -e sh -c '…': read the script as bash.
		for i, a := range args {
			if a.text == "-c" && i+1 < len(args) {
				p.nested(args[i+1:i+2], true)
				return
			}
		}
		p.res.unknown++
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
	case "new":
		k := Redirect
		switch {
		case itemType == "" || strings.HasPrefix("file", itemType):
		case strings.HasPrefix("symboliclink", itemType), strings.HasPrefix("hardlink", itemType), strings.HasPrefix("junction", itemType):
			k = Copy // a link at the path: what is there now
		default:
			return // a directory: no file
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
					p.addTok(psTok{text: filepath.Join(dv, nv), quoted: true}, k, true)
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
					p.addTok(psTok{text: filepath.Base(filepath.FromSlash(v)), quoted: true}, Copy, true)
				} else {
					p.res.unknown++
				}
			}
			return
		}
		for _, d := range dest {
			dv, ok := p.value(d)
			dpath, dok := p.abs(dv)
			if ok && dok && isDir(dpath) {
				// Into a directory that is there: each under its own name.
				for _, s := range ps {
					if v, ok := p.value(s); ok {
						p.res.add(filepath.Join(dpath, filepath.Base(filepath.FromSlash(v))), Copy)
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
				// The new name is in the item's directory, unless it is a path.
				if !strings.ContainsAny(nv, `/\`) {
					nv = filepath.Join(filepath.Dir(sv), nv)
				}
				p.addTok(psTok{text: nv, quoted: true}, Copy, true)
			}
		}
	case "cd", "pushd":
		ps, _ := path(0)
		if kind == "pushd" {
			p.stack = append(p.stack, p.dir)
		}
		if len(ps) == 0 {
			return
		}
		v, ok := p.value(ps[0])
		if !ok {
			p.dirOK = false
			return
		}
		if d, ok := p.abs(v); ok {
			p.dir = d
		} else {
			p.dirOK = false
		}
	case "popd":
		if n := len(p.stack); n > 0 {
			p.dir, p.stack = p.stack[n-1], p.stack[:n-1]
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
		if named["file"] != nil || named["f"] != nil || named["encodedcommand"] != nil || named["e"] != nil {
			p.res.unknown++
			return
		}
		if script == nil {
			script = at(0)
		}
		p.nested(script, false)
	case "cmd":
		// cmd /c "…": its redirections and commands read as bash's.
		for i, g := range pos {
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
				p.nestedText(strings.Join(rest, " "), true)
				return
			}
		}
		p.res.unknown++
	case "opaque":
		p.res.unknown++ // Invoke-Expression, Start-Process: stele cannot see what runs
	}
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
		ts, unknown := Targets(script, p.dir)
		for _, t := range ts {
			p.res.add(t.Path, t.Kind)
		}
		p.res.unknown += unknown
		return
	}
	q := &psReader{res: p.res, vars: map[string]string{}, dir: p.dir, dirOK: p.dirOK, depth: p.depth + 1}
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
	// A provider path: FileSystem::C:\x is the file; Env:x and the like are
	// not files.
	if i := strings.Index(v, "::"); i >= 0 {
		if !strings.HasSuffix(strings.ToLower(v[:i]), "filesystem") {
			return
		}
		v = v[i+2:]
	}
	if j := strings.IndexByte(v, ':'); j > 1 && !strings.ContainsAny(v[:j], `/\`) {
		return // env:, variable:, function: …: a drive that is not a disk
	}
	if device(v) {
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

// abs resolves a PowerShell path against the directory: absolute, ~ for the
// home directory, else relative. A path from the current drive's root, \x, is
// unknown, as is one with no directory to resolve against.
func (p *psReader) abs(v string) (string, bool) {
	v = filepath.FromSlash(v)
	if v == "~" || strings.HasPrefix(v, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
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

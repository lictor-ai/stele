package shell

import (
	"os"
	"path/filepath"
	"strings"
)

// PowerShellTargets returns the files a PowerShell command writes, resolved
// from cwd, in the order they appear, each once, as Targets does for bash
// (D77). Codex's shell runs PowerShell on Windows.
//
// It reads redirections (> >> *> n> and their >> forms), Set-Content,
// Add-Content, Out-File, Tee-Object, New-Item, Clear-Content, Copy-Item,
// Move-Item, Rename-Item and Remove-Item, with their usual aliases and
// abbreviated parameters, and [IO.File]'s write, copy, move and delete
// methods. It follows Set-Location and its aliases, and variables assigned
// a literal: $x = 'a.go'. Statements split on ; | && || and new lines, and a
// script block's statements are read too. A $(...) subexpression, $env:
// variable or a variable it does not know is counted as unknown, as is a path
// it cannot resolve. Scripts, & of a script, Invoke-Expression and
// redirections stele cannot tell from a word are not seen.
func PowerShellTargets(command, cwd string) (ts []Target, unknown int) {
	if len(command) > maxCommand {
		return nil, 1
	}
	r := &results{seen: map[string]int{}}
	ps := &psReader{res: r, vars: map[string]string{}, dir: cwd, dirOK: cwd != ""}
	for _, st := range psStatements(command) {
		ps.statement(st)
	}
	return r.out, r.unknown
}

// psToken is one word of a statement, or an operator.
type psToken struct {
	text    string
	op      bool // a redirection: > >> 2> *> ...
	quoted  bool // the word was quoted, all of it or part
	dynamic bool // it held something stele cannot know: $(...), $env:, an unknown variable
	param   bool // -Name, a parameter
	comma   bool // a word followed by a comma: an array goes on
}

// psStatements splits command into statements of tokens. Separators are
// ; | && || and new lines, and the braces of a script block, so its
// statements are read as if they were the command's own.
func psStatements(command string) [][]psToken {
	var out [][]psToken
	var cur []psToken
	end := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}
	s := command
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\n' || c == '\r' || c == ';' || c == '{' || c == '}':
			end()
			i++
		case c == '(' || c == ')':
			// A method's arguments, or a grouped expression: its words
			// stay in the statement.
			i++
		case c == '|' || (c == '&' && i+1 < len(s) && s[i+1] == '&'):
			end()
			i++
			if i < len(s) && (s[i] == '|' || s[i] == '&') {
				i++
			}
		case c == ' ' || c == '\t':
			i++
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '<' && i+1 < len(s) && s[i+1] == '#':
			j := strings.Index(s[i+2:], "#>")
			if j < 0 {
				return append(out, cur)
			}
			i += j + 4
		case c == ',':
			if n := len(cur); n > 0 {
				cur[n-1].comma = true
			}
			i++
		case c == '`' && i+1 < len(s) && (s[i+1] == '\n' || s[i+1] == '\r'):
			// A line continued.
			i += 2
			if i < len(s) && s[i] == '\n' {
				i++
			}
		default:
			if op, n := psRedirect(s[i:]); n > 0 {
				cur = append(cur, psToken{text: op, op: true})
				i += n
				continue
			}
			t, n := psWord(s[i:])
			if n == 0 {
				i++
				continue
			}
			cur = append(cur, t)
			i += n
		}
	}
	end()
	return out
}

// psRedirect reads a redirection operator at the start of s: > >> *> *>>,
// or a stream number before either. 2>&1 and the like merge streams and
// write no file: they are read as a word, then dropped.
func psRedirect(s string) (string, int) {
	i := 0
	if len(s) > 0 && (s[0] == '*' || ('1' <= s[0] && s[0] <= '6')) {
		i = 1
	}
	if i >= len(s) || s[i] != '>' {
		return "", 0
	}
	i++
	if i < len(s) && s[i] == '>' {
		i++
	}
	if i < len(s) && s[i] == '&' {
		// A merge: 2>&1. Skip it and its stream number.
		j := i + 1
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		return "&", j
	}
	return s[:i], i
}

// psWord reads one word: bare text, with quoted parts, up to a space or an
// operator. Single quotes are literal, ” a quote; double quotes take `
// escapes and variables; outside quotes ` escapes the next character.
func psWord(s string) (psToken, int) {
	var t psToken
	var b strings.Builder
	i := 0
	if s[0] == '-' && len(s) > 1 && letter(s[1]) {
		t.param = true
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' || c == '|' || c == ',' ||
			c == '{' || c == '}' || c == '(' || c == ')':
			t.text = b.String()
			return t, i
		case c == '>' && !t.quoted && b.Len() > 0 && !t.param:
			t.text = b.String()
			return t, i
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
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

// psReader reads statements, keeping the directory and variables.
type psReader struct {
	res   *results
	vars  map[string]string
	dir   string
	dirOK bool
}

// value expands the variables in a word. ok is false when it holds one
// stele does not know.
func (p *psReader) value(t psToken) (string, bool) {
	if t.dynamic {
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

// psCommands are the cmdlets, by every name and alias, that touch files.
var psCommands = map[string]string{
	"set-content": "set", "sc": "set",
	"add-content": "set", "ac": "set",
	"out-file":   "out",
	"tee-object": "tee", "tee": "tee",
	"new-item": "new", "ni": "new",
	"clear-content": "clear", "clc": "clear",
	"copy-item": "copy", "cp": "copy", "copy": "copy", "cpi": "copy",
	"move-item": "move", "mv": "move", "move": "move", "mi": "move",
	"rename-item": "rename", "ren": "rename", "rni": "rename",
	"remove-item": "remove", "rm": "remove", "del": "remove", "ri": "remove", "erase": "remove", "rd": "remove", "rmdir": "remove",
	"set-location": "cd", "cd": "cd", "sl": "cd", "chdir": "cd", "push-location": "cd", "pushd": "cd",
}

// psParams are, per kind of command, its parameters that take a value, the
// path ones first; the rest of a command's parameters are switches.
var psParams = map[string][]string{
	"set":    {"path", "literalpath", "value", "encoding", "stream", "filter", "include", "exclude", "credential", "itemtype", "delimiter"},
	"out":    {"filepath", "literalpath", "inputobject", "encoding", "width"},
	"tee":    {"filepath", "literalpath", "inputobject", "variable", "encoding"},
	"new":    {"path", "name", "itemtype", "value", "credential", "target"},
	"clear":  {"path", "literalpath", "filter", "include", "exclude", "stream", "credential"},
	"copy":   {"path", "literalpath", "destination", "filter", "include", "exclude", "credential", "tosession", "fromsession"},
	"move":   {"path", "literalpath", "destination", "filter", "include", "exclude", "credential"},
	"rename": {"path", "literalpath", "newname", "credential"},
	"remove": {"path", "literalpath", "filter", "include", "exclude", "stream", "credential"},
	"cd":     {"path", "literalpath", "stackname"},
}

// psParam resolves a parameter as written, -Pa or -Path:x, to its full name
// among names: PowerShell takes any unique prefix. ok is false for none, or
// more than one.
func psParam(word string, names []string) (name, inline string, ok bool) {
	word = strings.ToLower(strings.TrimPrefix(word, "-"))
	word, inline, _ = strings.Cut(word, ":")
	var found []string
	for _, n := range names {
		if n == word {
			return n, inline, true
		}
		if strings.HasPrefix(n, word) {
			found = append(found, n)
		}
	}
	if len(found) == 1 {
		return found[0], inline, true
	}
	return "", "", false
}

// statement reads one statement.
func (p *psReader) statement(ts []psToken) {
	// Redirections first: they write whatever the command is.
	var words []psToken
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if !t.op {
			words = append(words, t)
			continue
		}
		if t.text == "&" || i+1 >= len(ts) {
			continue
		}
		i++
		p.add(ts[i], Redirect, false)
	}
	if len(words) == 0 {
		return
	}
	// $x = 'literal'
	if len(words) >= 3 && words[1].text == "=" && strings.HasPrefix(words[0].text, "\x00") {
		name := strings.ToLower(strings.Trim(words[0].text, "\x00"))
		if v, ok := p.value(words[2]); ok && len(words) == 3 && !strings.Contains(name, ":") {
			p.vars[name] = v
		} else {
			delete(p.vars, name)
		}
		return
	}
	// & cmd, . cmd: the command is the next word.
	if (words[0].text == "&" || words[0].text == ".") && !words[0].quoted && len(words) > 1 {
		words = words[1:]
	}
	head := words[0].text
	if strings.HasPrefix(head, "[") {
		p.dotnet(head, words[1:])
		return
	}
	kind, ok := psCommands[strings.ToLower(head)]
	if !ok {
		return
	}
	p.cmdlet(kind, words[1:])
}

// cmdlet reads a cmdlet's arguments: named ones, then positional.
func (p *psReader) cmdlet(kind string, args []psToken) {
	names := psParams[kind]
	named := map[string][]psToken{}
	var pos [][]psToken
	itemType := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.param && !a.quoted {
			name, inline, ok := psParam(a.text, names)
			if !ok {
				continue // a switch, or a parameter stele does not know
			}
			var vals []psToken
			if inline != "" {
				vals = append(vals, psToken{text: inline})
			} else {
				for i+1 < len(args) {
					i++
					vals = append(vals, args[i])
					if !args[i].comma {
						break
					}
				}
			}
			named[name] = append(named[name], vals...)
			if name == "itemtype" && len(vals) > 0 {
				itemType = strings.ToLower(vals[0].text)
			}
			continue
		}
		group := []psToken{a}
		for a.comma && i+1 < len(args) {
			i++
			a = args[i]
			group = append(group, a)
		}
		pos = append(pos, group)
	}
	at := func(n int) []psToken {
		if n < len(pos) {
			return pos[n]
		}
		return nil
	}
	path := func(first int) ([]psToken, bool) {
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
	switch kind {
	case "set", "out", "tee":
		ps, lit := path(0)
		p.addAll(ps, Redirect, lit)
	case "new":
		if itemType != "" && !strings.HasPrefix("file", itemType) {
			return // a directory, a link: no file's content
		}
		ps, lit := path(0)
		if name := named["name"]; name != nil {
			// -Name is a file under -Path, a directory.
			for _, d := range ps {
				dv, ok := p.value(d)
				for _, n := range name {
					nv, nok := p.value(n)
					if !ok || !nok {
						p.res.unknown++
						continue
					}
					p.add(psToken{text: filepath.Join(dv, nv), quoted: true}, Redirect, true)
				}
			}
			if ps == nil {
				p.addAll(name, Redirect, true)
			}
			return
		}
		p.addAll(ps, Redirect, lit)
	case "clear":
		ps, lit := path(0)
		p.addAll(ps, Copy, lit)
	case "remove":
		ps, lit := path(0)
		p.addAll(ps, Remove, lit)
	case "copy", "move":
		ps, lit := path(0)
		if kind == "move" {
			p.addAll(ps, Remove, lit)
		}
		p.addAll(p.second(named, "destination", at), Copy, true)
	case "rename":
		ps, lit := path(0)
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
				p.add(psToken{text: nv, quoted: true}, Copy, true)
			}
		}
	case "cd":
		ps, _ := path(0)
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
	}
}

// second is a command's second argument, a destination or a new name: the
// parameter name, or else the first positional one when the path was named,
// the second when it was not.
func (p *psReader) second(named map[string][]psToken, name string, at func(int) []psToken) []psToken {
	if v := named[name]; v != nil {
		return v
	}
	if named["path"] != nil || named["literalpath"] != nil {
		return at(0)
	}
	return at(1)
}

// dotnet reads a call of a .NET method on [IO.File] or [System.IO.File]:
// [IO.File]::WriteAllText('a.go', ...). Its arguments are the statement's
// next words, the parentheses split off.
func (p *psReader) dotnet(head string, args []psToken) {
	typ, method, ok := strings.Cut(head, "]::")
	if !ok {
		return
	}
	typ = strings.ToLower(strings.TrimPrefix(typ, "["))
	if typ != "io.file" && typ != "system.io.file" {
		return
	}
	var kinds []Kind
	switch strings.ToLower(method) {
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
	for i, k := range kinds {
		if i >= len(args) {
			return
		}
		if k >= 0 {
			p.add(args[i], k, true)
		}
	}
}

// addAll adds each path of an array.
func (p *psReader) addAll(ts []psToken, k Kind, literal bool) {
	for _, t := range ts {
		p.add(t, k, literal)
	}
}

// add adds the file word names. A path with wildcards, not a literal one,
// adds what it matches now.
func (p *psReader) add(t psToken, k Kind, literal bool) {
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

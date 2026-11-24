package shell

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// parser reads one command line: a list of pipelines of simple commands. It
// lexes and acts in one pass, so a cd changes the directory of the commands
// after it.
type parser struct {
	src   string
	i     int
	depth int
	err   bool // a syntax error: nothing after it is guessed

	res  *results
	vars map[string]string // NAME=literal assignments seen so far
	// dyn are the variables set to something unknown. allVars stands for
	// every variable not in vars, sourced for all but HOME; sticky+NAME and
	// nameref+NAME mark names
	// whose later assignments are unknown too.
	dyn map[string]bool
	// fold are the environment variables a PowerShell command around this
	// script set, lower case: unknown here in any case. "\x00" stands for all.
	fold map[string]bool

	dir   string // the directory relative paths resolve against
	dirOK bool   // false once a cd went somewhere unknown

	heredocs []heredoc // pending, read at the next newline

	// A function body runs only when the function is called: its cd does not
	// move the directory of what follows it.
	funcNext bool   // the next { … } is a function body
	braces   int    // { … } groups open
	bodies   []body // function bodies open, innermost last
}

// body is a function body being read: where it began, and the directory to
// go back to at its end.
type body struct {
	braces int
	dir    string
	dirOK  bool
}

// The states of a case … esac being read.
const (
	caseHead    = iota // the subject, up to in
	casePattern        // a pattern, up to its )
	caseBody           // the commands of a pattern, up to ;; or esac
)

type heredoc struct {
	delim  string
	strip  bool // <<-: leading tabs are not part of a line
	expand bool // the delimiter is unquoted: the body's substitutions run
}

// word is one shell word, quotes removed and what can be expanded expanded.
type word struct {
	raw     string // as written
	text    string
	dynamic bool   // holds an expansion stele cannot resolve
	glob    bool   // holds an unquoted * ? or [
	procsub bool   // <(…) or >(…): a file descriptor, not a file
	assign  string // NAME, when the word is NAME=value, NAME+=value or NAME[i]=value
	value   string // the value, when assign is set and partial is not
	partial bool   // NAME+= or NAME[i]=: NAME keeps part of what it held
}

// plain reports whether the word is written as its text: no quote, no
// expansion. Only a plain word can be a keyword.
func (w word) plain() bool { return w.raw == w.text && !w.dynamic }

type tokKind int

const (
	tEOF tokKind = iota
	tWord
	tOp    // ; & && || | |& ;; newline ( ) ((
	tRedir // a redirection with its fd and target word
)

type token struct {
	kind    tokKind
	op      string
	fd      string
	w       word
	hasWord bool
}

// command is one simple command being read.
type command struct {
	words   []word
	redirs  []token
	assigns int // how many words start it that are assignments
}

// reset empties c, keeping its arrays for the next command.
func (c *command) reset() {
	c.words, c.redirs, c.assigns = c.words[:0], c.redirs[:0], 0
}

// add appends a word.
func (c *command) add(w word) {
	if w.assign != "" && c.assigns == len(c.words) {
		c.assigns++
	}
	c.words = append(c.words, w)
}

// keywords may stand before a command; they are not its name.
var keywords = map[string]bool{
	"!": true, "if": true, "then": true, "else": true, "elif": true, "fi": true,
	"do": true, "done": true, "while": true, "until": true, "time": true, "esac": true,
}

// list reads commands until the end, or until term (")") closes them.
func (p *parser) list(term string) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxDepth {
		p.err = true
		return
	}
	var c command
	var cases []int // the case … esac open in this list, innermost last
	prevPipe, fnName := false, false
	for !p.err {
		t := p.next()
		if p.err {
			return // the command being read is dropped
		}
		n := len(cases)
		switch t.kind {
		case tEOF:
			p.finish(&c, prevPipe)
			return
		case tWord:
			if n > 0 && cases[n-1] != caseBody {
				// The subject or a pattern: not a command.
				switch {
				case !t.w.plain():
				case cases[n-1] == caseHead && t.w.text == "in":
					cases[n-1] = casePattern
				case cases[n-1] == casePattern && t.w.text == "esac":
					cases = cases[:n-1]
				}
				continue
			}
			if fnName {
				fnName = false // function NAME
				continue
			}
			if len(c.words) == 0 && t.w.plain() {
				switch t.w.text {
				case "case":
					p.funcNext = false
					cases = append(cases, caseHead)
					continue
				case "esac":
					if n > 0 {
						cases = cases[:n-1]
					}
					continue
				case "function":
					p.funcNext, fnName = true, true
					continue
				case "{":
					if p.funcNext {
						p.bodies = append(p.bodies, body{p.braces, p.dir, p.dirOK})
						p.funcNext = false
					}
					p.braces++
					continue
				case "}":
					p.braces--
					if b := len(p.bodies); b > 0 && p.bodies[b-1].braces == p.braces {
						p.dir, p.dirOK = p.bodies[b-1].dir, p.bodies[b-1].dirOK
						p.bodies = p.bodies[:b-1]
					}
					continue
				case "[[":
					p.funcNext = false
					p.skipWords("]]")
					continue
				}
				if keywords[t.w.text] {
					if t.w.text == "time" {
						p.skipTimeOptions()
					}
					continue
				}
			}
			p.funcNext = false
			c.add(t.w)
		case tRedir:
			c.redirs = append(c.redirs, t)
		case tOp:
			if n > 0 {
				switch st := cases[n-1]; {
				case st == casePattern && t.op == ")":
					cases[n-1] = caseBody
					c.reset()
					continue
				case st == casePattern && (t.op == "(" || t.op == "|" || t.op == "\n"):
					continue // (a|b): parts of the pattern
				case st == caseBody && (t.op == ";;" || t.op == ";&" || t.op == ";;&"):
					p.finish(&c, prevPipe)
					prevPipe = false
					cases[n-1] = casePattern
					continue
				case st == caseHead:
					continue
				}
			}
			switch t.op {
			case "(":
				switch {
				case p.arrayStart(&c):
					p.array()
					c.words[len(c.words)-1].dynamic = true
					continue
				case len(c.words) > 0:
					// NAME(): a function definition.
					if q := p.next(); q.kind != tOp || q.op != ")" {
						p.err = true
						return
					}
					c.reset()
					p.funcNext = true
					continue
				case p.funcNext:
					// function NAME(), or a function's body in a subshell.
					p.skipBlanks()
					if p.has(")") {
						p.i++
						continue
					}
					p.funcNext = false
				}
				dir, ok := p.dir, p.dirOK
				p.list(")")
				p.dir, p.dirOK = dir, ok // a subshell's cd stays in it
			case "((":
				p.funcNext = false
				p.skipArith()
			case ")":
				if term == ")" {
					p.finish(&c, prevPipe)
					return
				}
				c.reset() // a case pattern, such as a)
			default:
				pipe := t.op == "|" || t.op == "|&"
				p.finish(&c, prevPipe || pipe || t.op == "&")
				prevPipe = pipe
			}
		}
	}
}

// declarations are the builtins whose arguments may be arrays.
var declarations = map[string]bool{
	"alias": true, "declare": true, "eval": true, "export": true,
	"let": true, "local": true, "readonly": true, "typeset": true,
}

// arrayStart reports whether the ( just read opens an array: it touches c's
// last word, which is NAME= or the like, and that word stands where an
// assignment can. Anywhere else bash calls it a syntax error.
func (p *parser) arrayStart(c *command) bool {
	last := len(c.words) - 1
	if last < 0 || p.src[p.i-2] != '=' {
		return false
	}
	if end, _ := assignEnd(c.words[last].raw); end == 0 || end != len(c.words[last].raw)-1 {
		return false
	}
	if c.assigns >= last {
		return true
	}
	first := c.words[c.assigns]
	return first.plain() && declarations[first.text]
}

// array reads the elements of NAME=(…), after the opening paren, up to its
// closing one. Reading a word runs its substitutions; only words may stand
// there.
func (p *parser) array() {
	for !p.err {
		switch t := p.next(); {
		case t.kind == tWord, t.kind == tOp && t.op == "\n":
		case t.kind == tOp && t.op == ")":
			return
		default:
			p.err = true
		}
	}
}

// skipTimeOptions skips what the time keyword takes: one -p, then one --.
// Anything else after it is the command, even a word starting with -.
func (p *parser) skipTimeOptions() {
	for _, opt := range []string{"-p", "--"} {
		p.skipBlanks()
		end := p.i + len(opt)
		if p.has(opt) && (end == len(p.src) || strings.IndexByte(" \t\r\n;&|()<>", p.src[end]) >= 0) {
			p.i = end
		}
	}
}

// skipWords reads tokens up to a plain word end: [[ … ]], whose < and >
// compare.
func (p *parser) skipWords(end string) {
	for !p.err {
		t := p.next()
		if t.kind == tEOF || t.kind == tWord && t.w.plain() && t.w.text == end {
			return
		}
	}
}

// skipArith skips an arithmetic command after its "((", up to "))". The
// variables it assigns are unknown from then on.
func (p *parser) skipArith() {
	depth, start := 0, p.i
	for p.i < len(p.src) {
		switch p.src[p.i] {
		case '(':
			depth++
		case ')':
			if depth == 0 && p.i+1 < len(p.src) && p.src[p.i+1] == ')' {
				p.arith(p.src[start:p.i])
				p.i += 2
				return
			}
			depth--
		}
		p.i++
	}
	p.err = true
}

// next returns the next token.
func (p *parser) next() token {
	for {
		p.skipBlanks()
		if p.i >= len(p.src) {
			return token{kind: tEOF}
		}
		c := p.src[p.i]
		switch c {
		case '#':
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
			continue
		case '\n':
			p.i++
			p.readHeredocs()
			return token{kind: tOp, op: "\n"}
		case ';':
			return p.op(";;&", ";;", ";&", ";")
		case '&':
			if p.has("&>") {
				return p.redirect("")
			}
			return p.op("&&", "&")
		case '|':
			return p.op("||", "|&", "|")
		case '(':
			return p.op("((", "(")
		case ')':
			return p.op(")")
		case '<', '>':
			if p.i+1 < len(p.src) && p.src[p.i+1] == '(' {
				p.i += 2
				p.sub()
				return token{kind: tWord, w: word{dynamic: true, procsub: true}}
			}
			return p.redirect("")
		}
		start := p.i
		w := p.readWord()
		if p.err {
			return token{kind: tEOF}
		}
		if digits(p.src[start:p.i]) && p.i < len(p.src) && (p.src[p.i] == '<' || p.src[p.i] == '>') {
			return p.redirect(w.text)
		}
		return token{kind: tWord, w: w}
	}
}

// skipBlanks skips spaces, tabs, carriage returns and line continuations.
func (p *parser) skipBlanks() {
	for p.i < len(p.src) {
		switch {
		case p.src[p.i] == ' ' || p.src[p.i] == '\t' || p.src[p.i] == '\r':
			p.i++
		case p.has("\\\n"):
			p.i += 2
		default:
			return
		}
	}
}

func (p *parser) has(s string) bool { return strings.HasPrefix(p.src[p.i:], s) }

// op returns the first of ops that the input starts with.
func (p *parser) op(ops ...string) token {
	for _, o := range ops {
		if p.has(o) {
			p.i += len(o)
			return token{kind: tOp, op: o}
		}
	}
	return token{kind: tEOF} // not reached: the caller saw ops' first byte
}

// redirect reads a redirection operator and its word, after an fd if any.
func (p *parser) redirect(fd string) token {
	t := token{kind: tRedir, fd: fd}
	for _, o := range []string{"&>>", "&>", ">>", ">|", ">&", "<<<", "<<-", "<<", "<>", "<&", ">", "<"} {
		if p.has(o) {
			t.op = o
			p.i += len(o)
			break
		}
	}
	p.skipBlanks()
	if p.i >= len(p.src) || strings.IndexByte(" \t\n;&|()<>", p.src[p.i]) >= 0 {
		return t // no word: nothing to write
	}
	t.w, t.hasWord = p.readWord(), true
	if t.op == "<<" || t.op == "<<-" {
		quoted := t.w.raw != t.w.text || strings.ContainsAny(t.w.raw, `'"\`)
		p.heredocs = append(p.heredocs, heredoc{delim: t.w.text, strip: t.op == "<<-", expand: !quoted})
	}
	return t
}

// readHeredocs reads the bodies of the heredocs pending at a newline. A body
// is text, not commands; but when its delimiter is unquoted, its $(…) and
// backticks run, so they are read for targets.
func (p *parser) readHeredocs() {
	pending := p.heredocs
	p.heredocs = nil
	for _, h := range pending {
		for p.i < len(p.src) && !p.err {
			end := lineEnd(p.src, p.i)
			line := strings.TrimSuffix(p.src[p.i:end], "\r")
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				p.i = min(end+1, len(p.src))
				break
			}
			if h.expand {
				p.substitutions(end)
				// A substitution may have run past this line: go on from
				// the end of the line it stopped in.
				end = lineEnd(p.src, max(p.i, end))
			}
			p.i = min(end+1, len(p.src))
		}
	}
}

// lineEnd is the index of the newline ending the line at i, or len(s).
func lineEnd(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	if n := strings.IndexByte(s[i:], '\n'); n >= 0 {
		return i + n
	}
	return len(s)
}

// substitutions reads the $(…) and backticks of a heredoc line, up to end.
func (p *parser) substitutions(end int) {
	for p.i < end && !p.err {
		switch {
		case p.src[p.i] == '\\':
			p.i += 2
		case p.has("$(("):
			p.i += 3
			p.skipArith()
		case p.has("$("):
			p.i += 2
			p.sub()
		case p.src[p.i] == '`':
			var w word
			p.backtick(&w)
		default:
			p.i++
		}
	}
}

// readWord reads one word, removing quotes and expanding what it can.
func (p *parser) readWord() word {
	var w word
	var b strings.Builder
	start := p.i
	for p.i < len(p.src) && !p.err {
		c := p.src[p.i]
		if strings.IndexByte(" \t\r\n;&|()<>", c) >= 0 {
			break
		}
		switch c {
		case '\\':
			switch {
			case p.i+1 >= len(p.src):
				p.i++
			case p.src[p.i+1] == '\n':
				p.i += 2
			default:
				b.WriteByte(p.src[p.i+1])
				p.i += 2
			}
		case '\'':
			end := strings.IndexByte(p.src[p.i+1:], '\'')
			if end < 0 {
				p.err = true
				break
			}
			b.WriteString(p.src[p.i+1 : p.i+1+end])
			p.i += end + 2
		case '"':
			p.i++
			p.doubleQuoted(&b, &w)
		case '$':
			p.dollar(&b, &w, false)
		case '`':
			p.backtick(&w)
		case '*', '?', '[':
			w.glob = true
			b.WriteByte(c)
			p.i++
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	w.raw, w.text = p.src[start:p.i], b.String()
	if end, partial := assignEnd(w.raw); end > 0 {
		w.assign, w.partial = w.raw[:nameLen(w.raw)], partial
		if !partial {
			w.value = w.text[end+1:]
		}
	}
	// ~ or ~/… is the home directory; ~user/… is not known.
	if strings.HasPrefix(w.raw, "~") {
		if home := p.home(); home != "" && (w.raw == "~" || w.raw[1] == '/') && p.res.expand(len(home)) {
			w.text = home + w.text[1:]
		} else {
			w.dynamic = true
		}
	}
	return w
}

// doubleQuoted reads the inside of "…", after the opening quote.
func (p *parser) doubleQuoted(b *strings.Builder, w *word) {
	for p.i < len(p.src) && !p.err {
		c := p.src[p.i]
		switch c {
		case '"':
			p.i++
			return
		case '\\':
			if p.i+1 < len(p.src) && strings.IndexByte("$`\"\\\n", p.src[p.i+1]) >= 0 {
				if p.src[p.i+1] != '\n' {
					b.WriteByte(p.src[p.i+1])
				}
				p.i += 2
				continue
			}
			b.WriteByte(c)
			p.i++
		case '$':
			p.dollar(b, w, true)
		case '`':
			p.backtick(w)
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	p.err = true // no closing quote
}

// dollar reads an expansion at $.
func (p *parser) dollar(b *strings.Builder, w *word, quoted bool) {
	p.i++
	if p.i >= len(p.src) {
		b.WriteByte('$')
		return
	}
	c := p.src[p.i]
	switch {
	case p.has("(("):
		p.i += 2
		p.skipArith()
		w.dynamic = true
	case c == '(':
		p.i++
		p.sub()
		w.dynamic = true
	case c == '[': // $[ … ], old arithmetic
		depth, start := 0, p.i
		for p.i < len(p.src) {
			switch p.src[p.i] {
			case '[':
				depth++
			case ']':
				depth--
			}
			p.i++
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			p.err = true
		}
		p.arith(p.src[start:p.i])
		w.dynamic = true
	case c == '{':
		end := strings.IndexByte(p.src[p.i:], '}')
		if end < 0 {
			p.err = true
			return
		}
		name := p.src[p.i+1 : p.i+end]
		p.i += end + 1
		if n := nameLen(name); n == len(name) && name != "" {
			p.expand(b, w, name)
		} else {
			// ${X=v} and ${X:=v} assign X; ${a[i++]} and ${s:i=1} are
			// arithmetic.
			if n > 0 && (strings.HasPrefix(name[n:], "=") || strings.HasPrefix(name[n:], ":=")) {
				p.forget(name[:n])
			}
			p.arith(name[n:])
			w.dynamic = true
		}
	case c == '\'' && !quoted:
		p.ansiC(b)
	case nameStart(c):
		n := nameLen(p.src[p.i:])
		p.expand(b, w, p.src[p.i:p.i+n])
		p.i += n
	case strings.IndexByte("0123456789@*#?$!-", c) >= 0:
		p.i++
		w.dynamic = true
	default:
		b.WriteByte('$')
	}
}

// expand writes the value of variable name, from an assignment earlier in
// the command or the environment.
func (p *parser) expand(b *strings.Builder, w *word, name string) {
	switch name {
	case "PWD": // where the command has moved to, not where stele runs
		if !p.dirOK || p.dyn[name] {
			w.dynamic = true
			return
		}
		v, ok := p.vars[name]
		if !ok {
			if p.dyn[allVars] || p.dyn[sourced] {
				w.dynamic = true
				return
			}
			v = p.dir
		}
		if !p.res.expand(len(v)) {
			w.dynamic = true
			return
		}
		b.WriteString(v)
		return
	case "OLDPWD":
		w.dynamic = true
		return
	}
	if p.dyn[name] {
		w.dynamic = true
		return
	}
	v, ok := p.vars[name]
	if !ok {
		if p.dyn[allVars] || p.dyn[sourced] && name != "HOME" || p.folded(name) { // set by a source, or by the PowerShell command around this script
			w.dynamic = true
			return
		}
		if p.res.grown > maxExpand { // spent: not even looked up
			w.dynamic = true
			return
		}
		v = p.res.env(name)
		ok = v != ""
	}
	if !ok || !p.res.expand(len(v)) {
		w.dynamic = true
		return
	}
	b.WriteString(v)
}

// ansiC reads $'…', after the $.
func (p *parser) ansiC(b *strings.Builder) {
	p.i++
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case c == '\'':
			p.i++
			return
		case c == '\\' && p.i+1 < len(p.src):
			b.WriteByte(p.src[p.i+1])
			p.i += 2
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	p.err = true
}

// sub reads the commands of $(…), <(…) or >(…), after the opening paren.
// They run in a subshell, so their cd does not change this directory.
func (p *parser) sub() {
	dir, ok := p.dir, p.dirOK
	p.list(")")
	p.dir, p.dirOK = dir, ok
}

// backtick reads `…`: its inside is a command of its own.
func (p *parser) backtick(w *word) {
	var inner strings.Builder
	for j := p.i + 1; j < len(p.src); j++ {
		switch c := p.src[j]; {
		case c == '\\' && j+1 < len(p.src):
			if n := p.src[j+1]; n == '`' || n == '\\' || n == '$' {
				inner.WriteByte(n)
			} else {
				inner.WriteByte(c)
				inner.WriteByte(n)
			}
			j++
		case c == '`':
			p.i = j + 1
			w.dynamic = true
			q := &parser{src: inner.String(), res: p.res, vars: p.vars, dyn: p.dyn, fold: p.fold, dir: p.dir, dirOK: p.dirOK, depth: p.depth}
			q.list("")
			if q.err {
				p.err = true
			}
			return
		default:
			inner.WriteByte(c)
		}
	}
	p.err = true // no closing backtick
}

// finish acts on a simple command: its redirections, a cd, the files of
// tee, sed -i, perl -i and the like. inPipe is set for a command in a
// pipeline or in the background, whose cd changes nothing here.
func (p *parser) finish(c *command, inPipe bool) {
	defer c.reset()
	for _, r := range c.redirs {
		if !r.hasWord {
			continue
		}
		switch r.op {
		case ">", ">>", ">|", "<>", "&>", "&>>":
			p.add(r.w, Redirect)
		case ">&":
			if !digits(r.w.text) && r.w.text != "-" {
				p.add(r.w, Redirect)
			}
		}
	}
	words := p.unwrap(c.words)
	if len(words) == 0 {
		return
	}
	args := words[1:]
	switch name(words[0]) {
	case "cd", "pushd":
		if !inPipe {
			p.cd(args)
		}
	case "popd":
		if !inPipe {
			p.dirOK = false
		}
	case "export", "local", "declare", "readonly", "typeset":
		p.declare(name(words[0]), args)
	case "unset":
		p.unset(args)
	case "read":
		p.forget("REPLY")
		p.setVars(args, "adinNptu", "a", true)
	case "mapfile", "readarray":
		p.forget("MAPFILE")
		p.setVars(args, "dnOsuCc", "", true)
	case "printf":
		p.setVars(args, "v", "v", false)
	case "wait":
		p.setVars(args, "p", "p", false)
	case "getopts":
		p.forget("OPTARG")
		p.forget("OPTIND")
		if len(args) > 1 {
			p.forgetWord(args[1])
		}
	case "for", "select":
		// for X in …: X is each word in turn, and the last one after.
		if len(args) > 0 {
			p.forgetWord(args[0])
		}
	case "let":
		for _, a := range args {
			p.arith(a.raw)
		}
	case "source", ".":
		// The script may set any variable. HOME is taken to stay as it
		// was: a script such as venv/bin/activate doesn't move it.
		home, ok := p.vars["HOME"]
		clear(p.vars)
		if ok {
			p.vars["HOME"] = home
		}
		p.dyn[sourced] = true
	case "tee":
		for _, a := range operands(args, nil) {
			p.add(a, Tee)
		}
	case "sed":
		p.sed(args)
	case "perl":
		p.perl(args)
	case "rm":
		for _, a := range operands(args, nil) {
			p.add(a, Remove)
		}
	case "mv", "cp", "ln":
		p.copyTo(name(words[0]), args)
	case "truncate":
		for _, a := range operands(args, []string{"-s", "-r"}) {
			p.add(a, Copy)
		}
	case "touch":
		p.touch(args)
	case "mkdir":
		for _, a := range operands(args, []string{"-m", "--mode"}) {
			p.add(a, Create)
		}
	case "git":
		p.gitAdd(args)
	case "bash", "sh", "zsh", "dash", "ksh":
		if i, ok := shellScript(args); ok {
			p.nestedShell(args[i])
		}
	case "eval":
		p.eval(args, inPipe)
	}
}

// shellScript finds the command string of bash -c '…' and the like, after
// the shell's options: -c alone or in a cluster such as -lc, and the options
// before or after it, -o and -O taking a word. It returns the string's index
// in args. found is false with no -c, or no string after it: then the shell
// runs a script file, or nothing.
func shellScript(args []word) (i int, found bool) {
	c := false
	for ; i < len(args); i++ {
		a := args[i]
		t := a.text
		switch {
		case a.dynamic || len(t) < 2 || (t[0] != '-' && t[0] != '+'):
			return i, c
		case t == "--":
			return i + 1, c && i+1 < len(args)
		case strings.HasPrefix(t, "--"):
			if t == "--rcfile" || t == "--init-file" {
				i++
			}
		default:
			for j := 1; j < len(t); j++ {
				switch t[j] {
				case 'c':
					c = c || t[0] == '-'
				case 'o', 'O':
					i++
				}
			}
		}
	}
	return 0, false
}

// nestedShell reads the string of bash -c '…' as a command of its own, in a
// new shell: it starts in this directory, and a variable this command set is
// unknown there, since stele does not know whether it was exported. A string
// it cannot read whole counts as unknown. Its depth adds to this one's, so
// shells in shells stop at maxDepth.
func (p *parser) nestedShell(s word) {
	if s.dynamic || s.glob {
		p.res.unknown++
		return
	}
	dyn := map[string]bool{}
	for k := range p.vars {
		dyn[k] = true
	}
	for k := range p.dyn {
		dyn[k] = true
	}
	if !readBash(p.res, s.text, p.dir, p.dirOK, p.depth, dyn, p.fold) {
		p.res.unknown++
	}
}

// readBash reads script as bash, from dir, adding what it writes to res. It
// reports whether it read the script whole, within the nesting budget. dyn
// are the variables unknown in it; fold, the environment variables a
// PowerShell command set, in lower case, unknown in any case.
func readBash(res *results, script, dir string, dirOK bool, depth int, dyn, fold map[string]bool) bool {
	if !res.reread(len(script)) {
		return false
	}
	q := &parser{src: script, res: res, vars: map[string]string{}, dyn: dyn, fold: fold, dir: dir, dirOK: dirOK, depth: depth}
	q.list("")
	return !q.err
}

// eval reads its arguments, joined by spaces, as commands run here: their cd
// and assignments carry on, unless eval is in a pipeline. An argument stele
// cannot know is unknown, as is a string it cannot read whole.
func (p *parser) eval(args []word, inPipe bool) {
	if p.res.nested > maxNested {
		p.res.unknown++ // past the budget of nested scripts: not read
		return
	}
	// The string's length first: it is counted against the budget before
	// it is built, so many copies of a long variable are not joined (#169).
	n := len(args) - 1
	for _, a := range args {
		if a.dynamic || a.glob {
			p.res.unknown++
			if !inPipe {
				p.forgetAll() // it may set any variable
			}
			return
		}
		if n <= maxNested {
			n += len(a.text)
		}
	}
	if n <= 0 {
		return
	}
	if !p.res.reread(n) {
		p.res.unknown++
		return
	}
	var b strings.Builder
	b.Grow(n)
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a.text)
	}
	q := &parser{src: b.String(), res: p.res, vars: p.vars, dyn: p.dyn, fold: p.fold, dir: p.dir, dirOK: p.dirOK, depth: p.depth}
	if !inPipe {
		defer func() { p.dir, p.dirOK = q.dir, q.dirOK }()
	} else {
		// A pipeline's commands run in subshells: what eval sets stays there.
		q.vars, q.dyn = maps.Clone(p.vars), maps.Clone(p.dyn)
	}
	q.list("")
	if q.err {
		p.res.unknown++
	}
}

// unwrap drops what stands before the command's name: assignments, which it
// records, and wrappers such as sudo or env with their options.
func (p *parser) unwrap(words []word) []word {
	for len(words) > 0 {
		w := words[0]
		switch {
		case w.assign != "":
			p.assign(w)
			words = words[1:]
		case wrappers[name(w)] != nil:
			takes := wrappers[name(w)]
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0].text, "-") && words[0].text != "-" {
				opt := words[0].text
				words = words[1:]
				if opt == "--" {
					break
				}
				if takes[opt] && len(words) > 0 {
					words = words[1:]
				}
			}
			// timeout DURATION cmd: the duration is not the command.
			if n := positional[name(w)]; len(words) >= n {
				words = words[n:]
			}
		default:
			return words
		}
	}
	return words
}

// wrappers run the command after them; each maps the options that take an
// argument.
var wrappers = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true, "-r": true, "-t": true, "-U": true},
	"command": {},
	"env":     {"-u": true, "-C": true, "-S": true},
	"exec":    {"-a": true},
	"nohup":   {},
	"nice":    {"-n": true},
	"time":    {"-o": true, "-f": true},
	"timeout": {"-s": true, "-k": true},
	"builtin": {},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"ionice":  {"-c": true, "-n": true, "-t": false},
}

// positional counts the words a wrapper takes after its options, before the
// command.
var positional = map[string]int{"timeout": 1}

// name is a command word's name: its base name, without .exe.
func name(w word) string {
	n := filepath.Base(filepath.ToSlash(w.text))
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	if len(n) > 4 && strings.EqualFold(n[len(n)-4:], ".exe") {
		n = n[:len(n)-4]
	}
	return n
}

// assign records NAME=value for the expansions after it.
func (p *parser) assign(w word) {
	if w.dynamic || w.partial || p.dyn[sticky+w.assign] || p.dyn[nameref+w.assign] {
		p.forget(w.assign)
		return
	}
	p.vars[w.assign] = w.value
	delete(p.dyn, w.assign)
}

// Keys of parser.dyn that are not variable names (#189).
const (
	allVars = "\x00" // every variable not in vars is unknown
	sticky  = "\x01" // + NAME: declare -i, -l or -u changes what NAME is set to
	nameref = "\x02" // + NAME: declare -n: setting NAME sets another variable
	sourced = "\x03" // every variable not in vars but HOME is unknown
)

// forget makes variable name unknown from here on: a command such as unset,
// read or for set it to something stele doesn't know. Through a nameref any
// variable may change.
func (p *parser) forget(name string) {
	delete(p.vars, name)
	p.dyn[name] = true
	if p.dyn[nameref+name] {
		p.forgetAll()
	}
}

// forgetAll makes every variable unknown, as unset $n may unset any.
func (p *parser) forgetAll() {
	if len(p.vars) > 0 { // clear walks the map's whole capacity
		clear(p.vars)
	}
	p.dyn[allVars] = true
}

// forgetWord forgets the variable w names: NAME or NAME[i]. A name stele
// can't read may be any variable's.
func (p *parser) forgetWord(w word) {
	if w.dynamic {
		p.forgetAll()
		return
	}
	if n := nameLen(w.text); n > 0 {
		p.forget(w.text[:n])
	}
}

// setVars forgets the variables a builtin such as read sets: its operands,
// when operands is set, and the arguments of the options in names. takes are
// the options that take an argument. Options end at -- or the first operand.
func (p *parser) setVars(args []word, takes, names string, operands bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		t := a.text
		switch {
		case t == "--" && !a.dynamic:
			if operands {
				for _, o := range args[i+1:] {
					p.forgetWord(o)
				}
			}
			return
		case a.dynamic || len(t) < 2 || t[0] != '-':
			if operands {
				for _, o := range args[i:] {
					p.forgetWord(o)
				}
			}
			return
		}
		for j := 1; j < len(t); j++ {
			c := t[j]
			if strings.IndexByte(takes, c) < 0 {
				continue
			}
			arg := word{raw: t[j+1:], text: t[j+1:]}
			if arg.text == "" {
				if i+1 >= len(args) {
					return
				}
				i++
				arg = args[i]
			}
			if strings.IndexByte(names, c) >= 0 {
				p.forgetWord(arg)
			}
			break
		}
	}
}

// unset forgets the variables it names. unset is empty, not the
// environment's value, and a target such as $X/a.go would be /a.go: unknown
// rather than a guess. unset -f removes functions.
func (p *parser) unset(args []word) {
	funcs := false
	for i, a := range args {
		t := a.text
		switch {
		case t == "--" && !a.dynamic:
			if !funcs {
				for _, o := range args[i+1:] {
					p.forgetWord(o)
				}
			}
			return
		case !a.dynamic && len(t) > 1 && t[0] == '-':
			funcs = strings.Contains(t, "f") && !strings.Contains(t, "v")
		case !funcs:
			p.forgetWord(a)
		}
	}
}

// declare reads export, local, declare, readonly and typeset: NAME=literal
// is known, as an assignment; a value with -i, -l or -u is changed, as is
// any later one, and a nameref (-n) stands for another variable. A bare NAME
// for local, declare or typeset is a new variable, empty in a function.
func (p *parser) declare(cmd string, args []word) {
	attr, ref, opts := false, false, true
	for _, a := range args {
		t := a.text
		if opts && !a.dynamic && a.assign == "" {
			if t == "--" {
				opts = false
				continue
			}
			if len(t) > 1 && (t[0] == '-' || t[0] == '+') {
				attr = attr || strings.ContainsAny(t[1:], "ilu")
				ref = ref || cmd != "export" && t[0] == '-' && strings.Contains(t, "n")
				continue
			}
		}
		opts = false
		n := a.assign
		if n == "" && !a.dynamic {
			if l := nameLen(t); l > 0 && l == len(t) {
				n = t
			}
		}
		switch {
		case a.dynamic && a.assign == "":
			p.forgetAll() // declare $spec: any variable
			continue
		case n == "":
			continue
		}
		switch {
		case attr || ref:
			// Not forget: declaring a nameref sets no other variable.
			delete(p.vars, n)
			p.dyn[n] = true
			if attr {
				p.dyn[sticky+n] = true
			}
			if ref {
				p.dyn[nameref+n] = true
			}
		case a.assign != "":
			p.assign(a)
		case cmd == "local" || cmd == "declare" || cmd == "typeset":
			p.forget(n)
		}
	}
}

// arith forgets the variables an arithmetic expression assigns: NAME = …,
// NAME op= …, NAME++, ++NAME and the like, NAME[i] too. One assigned through
// an expansion, as in $n = 1, may be any variable.
func (p *parser) arith(s string) {
	// Each byte is looked at a bounded number of times: a run of name
	// bytes is skipped whole, and the ] a subscript ends at is found once
	// for every name before it (#191).
	close := -1 // the first ] at or after the last subscript looked for, or len(s)
	for i := 0; i < len(s); {
		if c := s[i]; '0' <= c && c <= '9' {
			// A number, or 1a…: not a name, nor any name inside it.
			j := i + 1
			for j < len(s) && (nameStart(s[j]) || '0' <= s[j] && s[j] <= '9') {
				j++
			}
			p.res.arith += j - i
			i = j
			continue
		}
		n := nameLen(s[i:])
		if n == 0 {
			p.res.arith++
			i++
			continue
		}
		p.res.arith += n
		name, end := s[i:i+n], i+n
		if end < len(s) && s[end] == '[' {
			if close < end {
				close = len(s)
				if c := strings.IndexByte(s[end:], ']'); c >= 0 {
					close = end + c
				}
				p.res.arith += close - end
			}
			if close < len(s) {
				end = close + 1
			}
		}
		before := strings.TrimRight(s[:i], " \t\n")
		after := strings.TrimLeft(s[end:], " \t\n")
		p.res.arith += i - len(before) + len(s) - end - len(after)
		if strings.HasSuffix(before, "++") || strings.HasSuffix(before, "--") || assignOp(after) {
			if strings.HasSuffix(before, "$") || strings.HasSuffix(before, "${") {
				p.forgetAll()
			} else {
				p.forget(name)
			}
		}
		i += n
	}
}

// assignOp reports whether s starts with an arithmetic assignment, ++ or --.
func assignOp(s string) bool {
	for _, op := range []string{"++", "--", "<<=", ">>=", "+=", "-=", "*=", "/=", "%=", "&=", "^=", "|="} {
		if strings.HasPrefix(s, op) {
			return true
		}
	}
	return strings.HasPrefix(s, "=") && !strings.HasPrefix(s, "==")
}

// cd moves the directory relative paths resolve against.
func (p *parser) cd(args []word) {
	args = operands(args, nil)
	if len(args) == 0 || args[0].dynamic || args[0].text == "-" {
		p.dirOK = false
		return
	}
	dir := ""
	if p.dirOK {
		dir = p.dir
	}
	if to, ok := Abs(dir, args[0].text); ok {
		p.dir = to
	} else {
		p.dirOK = false
	}
}

// operands returns the words after the options: those not starting with -,
// and every word after --. takes lists the options whose argument follows.
func operands(args []word, takes []string) []word {
	var out []word
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a.text == "--":
			return append(out, args[i+1:]...)
		case strings.HasPrefix(a.text, "-") && a.text != "-" && !a.dynamic:
			for _, t := range takes {
				if a.text == t {
					i++
				}
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// sed adds the files of sed -i.
func (p *parser) sed(args []word) {
	inPlace, script := false, false
	var ops []word
	for i := 0; i < len(args); i++ {
		a := args[i].text
		switch {
		case a == "--":
			ops = append(ops, args[i+1:]...)
			i = len(args)
		case !strings.HasPrefix(a, "-") || a == "-" || args[i].dynamic:
			ops = append(ops, args[i])
		case strings.HasPrefix(a, "--"):
			opt, _, hasValue := strings.Cut(a, "=")
			switch opt {
			case "--in-place":
				inPlace = true
			case "--expression", "--file":
				script = true
				if !hasValue {
					i++
				}
			case "--line-length":
				if !hasValue {
					i++
				}
			}
		default:
			c := a[1:]
		cluster:
			for j := 0; j < len(c); j++ {
				switch c[j] {
				case 'i': // the rest is the backup suffix
					inPlace = true
					// BSD sed takes the suffix as its own word: sed -i '' …
					if c == "i" && i+1 < len(args) && args[i+1].text == "" {
						i++
					}
					break cluster
				case 'e', 'f':
					script = true
					if j == len(c)-1 {
						i++
					}
					break cluster
				case 'l':
					if j == len(c)-1 {
						i++
					}
					break cluster
				}
			}
		}
	}
	if !inPlace {
		return
	}
	if !script && len(ops) > 0 {
		ops = ops[1:] // the script
	}
	for _, a := range ops {
		p.add(a, SedInPlace)
	}
}

// perl adds the files of perl -i.
func (p *parser) perl(args []word) {
	inPlace, script := false, false
	var ops []word
loop:
	for i := 0; i < len(args); i++ {
		a := args[i].text
		switch {
		case a == "--":
			ops = args[i+1:]
			break loop
		case !strings.HasPrefix(a, "-") || a == "-" || args[i].dynamic:
			ops = args[i:]
			break loop
		}
		c := a[1:]
	cluster:
		for j := 0; j < len(c); j++ {
			switch c[j] {
			case 'e', 'E': // the script follows
				script = true
				if j == len(c)-1 {
					i++
				}
				break cluster
			case 'i': // the rest is the backup extension
				inPlace = true
				break cluster
			case 'M', 'm', 'I', 'x', 'C', 'd', 'D', 'F':
				break cluster // the rest is the option's argument
			case 'l', '0':
				for j+1 < len(c) && '0' <= c[j+1] && c[j+1] <= '9' {
					j++
				}
			}
		}
	}
	if !inPlace {
		return
	}
	if !script && len(ops) > 0 {
		ops = ops[1:] // the script file
	}
	for _, a := range ops {
		p.add(a, PerlInPlace)
	}
}

// copyTo adds what mv, cp or ln leave behind: the last operand, or each
// source's name in it when it is a directory or given with -t. mv also
// removes its sources.
func (p *parser) copyTo(cmd string, args []word) {
	var dirWord *word
	var ops []word
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a.text == "--":
			ops = append(ops, args[i+1:]...)
			i = len(args)
		case a.text == "-t" && i+1 < len(args):
			dirWord = &args[i+1]
			i++
		case strings.HasPrefix(a.text, "--target-directory="):
			w := a
			w.text = strings.TrimPrefix(a.text, "--target-directory=")
			dirWord = &w
		case (a.text == "-S" || a.text == "--suffix") && i+1 < len(args):
			i++
		case strings.HasPrefix(a.text, "-") && a.text != "-" && !a.dynamic:
		default:
			ops = append(ops, a)
		}
	}
	srcs := ops
	if dirWord == nil && len(ops) > 0 {
		if len(ops) == 1 && cmd == "ln" {
			// ln x makes x's name here
			t := ops[0]
			t.text = filepath.Base(t.text)
			p.add(t, Copy)
			return
		}
		last := ops[len(ops)-1]
		srcs = ops[:len(ops)-1]
		if to, ok := p.resolve(last); !ok || !isDir(to) {
			if cmd == "mv" {
				for _, s := range srcs {
					p.add(s, Remove)
				}
			}
			p.add(last, Copy)
			return
		}
		dirWord = &last
	}
	if dirWord == nil {
		return
	}
	for _, s := range srcs {
		if s.dynamic || dirWord.dynamic {
			if cmd == "mv" {
				p.add(s, Remove)
			}
			p.res.unknown++
			continue
		}
		names := []string{s.text}
		if s.glob {
			matches, ok := p.glob(s.text)
			if !ok {
				p.res.unknown++ // over the budget: what it matches is not known
				continue
			}
			if len(matches) > 0 {
				names = matches
			}
		}
		for _, n := range names {
			if cmd == "mv" {
				p.add(word{text: n}, Remove)
			}
			t := *dirWord
			t.text = inDir(t.text, filepath.Base(n))
			t.glob = false
			p.add(t, Copy)
		}
	}
}

// inDir is name in dir, with dir left as written for Abs to resolve:
// filepath.Join would make Git Bash's /c/… into \c\… on Windows, and path.Join
// would clean a .. after a backslash, which separates there. A doubled
// separator is Abs's to clean. C: is the drive's current directory, not its root.
func inDir(dir, name string) string {
	if dir == "" || windows && strings.HasSuffix(dir, ":") {
		return dir + name
	}
	return dir + "/" + name
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// add adds w as a target, unless it is a device or a process substitution;
// unresolved, it is counted as unknown.
func (p *parser) add(w word, k Kind) {
	if w.procsub || device(w.text) {
		return
	}
	if w.glob && !w.dynamic {
		matches, ok := p.glob(w.text)
		if !ok {
			p.res.unknown++ // over the budget: what it matches is not known
			return
		}
		if len(matches) > 0 {
			for _, m := range matches {
				p.res.add(m, k)
			}
			return
		}
	}
	path, ok := p.resolve(w)
	if !ok {
		p.res.unknown++
		return
	}
	p.res.add(path, k)
}

// resolve returns w's absolute path.
func (p *parser) resolve(w word) (string, bool) {
	if w.dynamic {
		return "", false
	}
	dir := ""
	if p.dirOK {
		dir = p.dir
	}
	return Abs(dir, w.text)
}

// maxGlobEntries caps the directory entries a command's globs read, in all, and
// maxGlobSteps the directories they open and the fixed path segments they walk:
// a glob over a whole drive, thousands of globs over an empty directory or a
// path as long as a command must not cost seconds on every Bash call. The steps
// allow a directory opened and a fixed segment for each of the 2,000 entries,
// as in */src/*.go, and 1,000 for the paths of the words. Tests lower both.
var (
	maxGlobEntries = 2000
	maxGlobSteps   = 5000
)

// glob expands a pattern, segment by segment, within the budgets of directory
// entries and steps. ok is false past either, and for every glob after it. No
// match returns none, and the caller takes the word as written, as bash does.
// As in bash, a name starting with a dot needs a pattern starting with one, and
// a pattern ending in / matches directories only.
func (p *parser) glob(pattern string) (matches []string, ok bool) {
	if p.res.entries > maxGlobEntries || p.res.steps > maxGlobSteps {
		return nil, false
	}
	abs, ok := p.resolve(word{text: pattern})
	if !ok || p.res.globbed >= maxGlob {
		return nil, true
	}
	vol := filepath.VolumeName(abs)
	sep := string(filepath.Separator)
	rest := strings.TrimPrefix(abs[len(vol):], sep)
	segs := strings.Split(rest, sep)
	// The fixed segments before the first glob are a prefix of a clean path:
	// taking it whole keeps a long path linear.
	first, end := 0, 0
	for first < len(segs)-1 && !strings.ContainsAny(segs[first], "*?[") {
		end += len(segs[first]) + 1
		first++
	}
	base := vol + sep
	if first > 0 {
		base += rest[:end-1]
	}
	if p.res.steps += first; p.res.steps > maxGlobSteps {
		return nil, false
	}
	bases := []string{base}
	for i := first; i < len(segs); i++ {
		seg := segs[i]
		var next []string
		if !strings.ContainsAny(seg, "*?[") {
			// A run of fixed segments is joined once and added to each base
			// whole, still a step per segment: a copy of the path at each
			// one made a long path quadratic (#141). Only the last part of
			// the pattern is checked, so the run is one child.
			j := i + 1
			for j < len(segs) && !strings.ContainsAny(segs[j], "*?[") {
				j++
			}
			if slices.ContainsFunc(segs[i:j], tooLong) {
				// No such path exists: the last part's Lstat or the next
				// glob's Open fails on each base, a step each for the
				// Open. Count those steps, as the walk would, without a
				// path per base: a long segment joined to each of 2,000
				// matches took 1.5 GB (#143).
				n := j - i
				if j < len(segs) {
					n++
				}
				if p.res.steps += n * len(bases); p.res.steps > maxGlobSteps {
					p.res.steps = maxGlobSteps + 1 // where a step at a time stops
					return nil, false
				}
				return nil, true
			}
			run := strings.Join(segs[i:j], sep)
			for _, b := range bases {
				if p.res.steps += j - i; p.res.steps > maxGlobSteps {
					p.res.steps = maxGlobSteps + 1 // where a step at a time stops
					return nil, false
				}
				c := child(b, run)
				// A fixed last part must exist, as filepath.Glob has it.
				if j == len(segs) {
					if _, err := os.Lstat(c); err != nil {
						continue
					}
				}
				next = append(next, c)
			}
			if bases, i = next, j-1; len(bases) == 0 {
				return nil, true
			}
			continue
		}
		match := matchPattern(seg)
		for _, b := range bases {
			if p.res.steps++; p.res.steps > maxGlobSteps {
				return nil, false
			}
			f, err := os.Open(b)
			if err != nil {
				continue
			}
			es, _ := f.ReadDir(maxGlobEntries - p.res.entries + 1)
			f.Close()
			if p.res.entries += len(es); p.res.entries > maxGlobEntries {
				return nil, false
			}
			for _, e := range es {
				if m, _ := filepath.Match(match, e.Name()); m && (seg[0] == '.' || e.Name()[0] != '.') {
					next = append(next, child(b, e.Name()))
				}
			}
		}
		if bases = next; len(bases) == 0 {
			return nil, true
		}
	}
	if strings.HasSuffix(pattern, "/") {
		bases = slices.DeleteFunc(bases, func(b string) bool { return !isDir(b) })
	}
	sort.Strings(bases)
	if room := maxGlob - p.res.globbed; len(bases) > room {
		bases = bases[:room]
	}
	p.res.globbed += len(bases)
	return bases, true
}

// maxName is the longest a file name can be anywhere stele runs: FUSE's
// FUSE_NAME_MAX, the largest limit on Linux, where most file systems allow 255
// bytes. macOS allows 255 UTF-16 units and Windows 255 per name or stream,
// which are up to 765 bytes of UTF-8 each.
const maxName = 1024

// tooLong reports whether seg, a segment of a clean path, is longer than any
// name can be. On Windows a segment can be file:stream[:$DATA], each part a
// name of its own, and a last segment's trailing dots and spaces are dropped,
// so they don't count: such a segment, with a long tail of them, still takes
// the whole walk.
func tooLong(seg string) bool {
	if len(seg) <= maxName {
		return false
	}
	if !windows {
		return true
	}
	return slices.ContainsFunc(strings.Split(strings.TrimRight(seg, ". "), ":"), func(s string) bool { return len(s) > maxName })
}

// child is name in dir, a clean path: unlike filepath.Join it does not clean
// dir again.
func child(dir, name string) string {
	if os.IsPathSeparator(dir[len(dir)-1]) {
		return dir + name
	}
	return dir + string(filepath.Separator) + name
}

// matchPattern writes one segment of a bash glob as filepath.Match reads it:
// [!x] is its [^x], and a ] first in a set is a member, which it must be told
// by an escape. Windows has no escape, so there such a set matches nothing. A [
// with no closing ] stays as it is.
func matchPattern(seg string) string {
	if !strings.Contains(seg, "[") {
		return seg
	}
	var b strings.Builder
	for i := 0; i < len(seg); {
		switch {
		case seg[i] == '\\' && !windows && i+1 < len(seg):
			b.WriteString(seg[i : i+2])
			i += 2
			continue
		case seg[i] != '[':
			b.WriteByte(seg[i])
			i++
			continue
		}
		set := i + 1
		negate := set < len(seg) && (seg[set] == '!' || seg[set] == '^')
		if negate {
			set++
		}
		member := set < len(seg) && seg[set] == ']' // a ] first in the set
		end := set
		if member {
			end++
		}
		for end < len(seg) && seg[end] != ']' {
			if seg[end] == '\\' && !windows {
				end++
			}
			end++
		}
		if end >= len(seg) {
			b.WriteByte('[')
			i++
			continue
		}
		b.WriteByte('[')
		if negate {
			b.WriteByte('^')
		}
		if member && !windows {
			b.WriteByte('\\')
		}
		b.WriteString(seg[set : end+1])
		i = end + 1
	}
	return b.String()
}

// home is the home directory ~ stands for, or "" when the command set it:
// HOME, or USERPROFILE on Windows where HOME is unset (#181).
func (p *parser) home() string {
	if p.setHere("HOME") {
		return ""
	}
	h := p.res.env("HOME")
	if h == "" && windows {
		if p.setHere("USERPROFILE") {
			return ""
		}
		h = p.res.env("USERPROFILE")
	}
	return h
}

// setHere reports whether the command, or a PowerShell command around it,
// set the variable name.
func (p *parser) setHere(name string) bool {
	_, ok := p.vars[name]
	return ok || p.dyn[name] || p.dyn[allVars] || p.folded(name)
}

// folded reports whether a PowerShell command around this script set the
// environment variable name.
func (p *parser) folded(name string) bool {
	return len(p.fold) > 0 && (p.fold["\x00"] || p.fold[strings.ToLower(name)])
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func nameStart(c byte) bool { return c == '_' || letter(c) }

// assignEnd returns the index of the = when s starts with NAME=, NAME+=,
// NAME[…]= or NAME[…]+=, or 0. partial is set for all but the first.
func assignEnd(s string) (end int, partial bool) {
	n := nameLen(s)
	if n == 0 {
		return 0, false
	}
	if n < len(s) && s[n] == '[' {
		depth := 0
		for ; n < len(s); n++ {
			if s[n] == '[' {
				depth++
			} else if s[n] == ']' {
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			return 0, false
		}
		n++
		partial = true
	}
	if n < len(s) && s[n] == '+' {
		n++
		partial = true
	}
	if n < len(s) && s[n] == '=' {
		return n, partial
	}
	return 0, false
}

// nameLen is the length of the variable name s starts with.
func nameLen(s string) int {
	if s == "" || !nameStart(s[0]) {
		return 0
	}
	n := 1
	for n < len(s) && (nameStart(s[n]) || '0' <= s[n] && s[n] <= '9') {
		n++
	}
	return n
}

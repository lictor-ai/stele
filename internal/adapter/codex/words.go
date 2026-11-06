package codex

import (
	_ "embed"
	"runtime"
	"strings"
	"text/template"

	"github.com/lictor-ai/stele/internal/adapter"
)

// words name Codex's tools in stele's messages: it writes files with
// apply_patch, which the hooks see, and runs commands in its shell, which
// they do not. It cannot ask the user from a hook.
var words = adapter.Words{
	Agent:        "Codex",
	Shell:        "shell",
	WriteTools:   "apply_patch",
	WriteInstead: "apply_patch instead",
	NoAsk:        true,
}

// sessionContext is what the agent reads at the start of a session in a
// stele project: write files with apply_patch, which stele sees, not
// through the shell, which it does not (#50). Its examples are the shell's
// of the OS: PowerShell on Windows.
//
//go:embed assets/session-context.tmpl
var sessionContext string

var contextTemplate = template.Must(template.New("session-context").Parse(sessionContext))

// SessionContext returns the text the agent reads when a session starts on
// goos.
func SessionContext(goos string) string {
	ex := struct{ Examples, Reads string }{
		"`cat > file`, heredocs, `echo >`, `tee` or `sed -i`",
		"`cat a.go b.go`, `grep` or `go test`",
	}
	if goos == "windows" {
		ex.Examples = "`Set-Content`, `Out-File`, `Add-Content`, `>` or `[IO.File]::WriteAllText`"
		ex.Reads = "`Get-Content a.go`, `Select-String` or `go test`"
	}
	var b strings.Builder
	if err := contextTemplate.Execute(&b, ex); err != nil {
		panic(err) // the template is compiled in and tested
	}
	return strings.TrimSpace(b.String())
}

// sessionText is the session context on this OS.
func sessionText() string { return SessionContext(runtime.GOOS) }

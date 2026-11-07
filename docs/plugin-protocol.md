# Writing a stele plugin

A plugin is a program that gives stele something it doesn't do itself. A
**source** plugin gives it rules; a **checker** plugin checks code against
them in `stele check`; a **sink** plugin receives stele's log from
`stele push`. This page is protocol 1, the one this stele speaks
([ADR-0013](adr/0013-external-plugins.md)).

## The short version

stele starts your program once per call. It writes one JSON object to the
program's stdin and closes it. The program writes one JSON object to stdout
and exits 0. That's all.

```
stdin:  {"protocol": 1, "op": "read", "root": "/home/you/project", "config": {"file": "GUIDELINES.txt"}}
stdout: {"protocol": 1, "fingerprint": "9f2c…", "rules": [{"body": "Every exported function has a doc comment."}]}
```

## Naming and installing

Call the program `stele-source-<name>` or `stele-checker-<name>`, with `.exe`
on Windows, and put it on your `PATH`. `<name>` is lower-case letters, digits and `-`,
starting with a letter, at most 64. `stelemd`, `git` and `regex` are taken.

A project lists it in `stele.json`:

```json
{ "sources":  [{ "name": "<name>", "config": { … }, "timeout": "2m" }],
  "checkers": [{ "name": "<name>", "config": { … }, "timeout": "10m" }] }
```

stele runs it only if it is listed, and only from `PATH`: never from inside
the project, the git repository that holds it, or a relative `PATH` entry. On
Windows it must be an `.exe` or `.com`. It runs with no arguments and no
shell, with your environment, `PATH` and the library and module paths cut to
entries outside the project.

Your working directory is not the project: it is a directory of stele's own.
Use `root` from the request to find the project's files. If you change into
it and start programs by bare name, you may run one the project brought:
that is your risk.

Whatever you start stops when your call ends: stele stops your process
group on Unix and your job on Windows. Don't leave a process holding your
stdout; stele treats that as a failure.

## Every call

- The request has `"protocol": 1` and `"op"`. Ignore fields you don't know.
- The answer is exactly one JSON object with `"protocol": 1`, and nothing
  after it. stele ignores fields it doesn't know, so you can add your own.
- To fail, answer `{"protocol": 1, "error": "what went wrong"}` and exit 0.
  stele shows the message and exits 2.
- A program that doesn't speak protocol 1 answers
  `{"protocol": 2, "error": "unsupported protocol 1; speaks [2]"}`.
- Write anything for people on stderr. stele shows each line, starting with
  your program's name, up to 200 lines a call.
- `config` is what the project's `stele.json` set, `{}` when it set nothing.
  It comes from a committed file, possibly in a repository you didn't write:
  treat it as data. Never run a command it names.

## `describe`

Sent once per run of stele, before any other call.

```
{"protocol": 1, "op": "describe"}
{"protocol": 1, "kind": "source", "protocols": [1], "version": "0.1.0"}
```

`kind` must be `source` for a `stele-source-` program, `checker` for a
`stele-checker-` one, `sink` for a `stele-sink-` one. `protocols` must hold
1. `version` is your program's, 1 to 64 printable characters.

## `read`

Sent by `stele compile`, `stele check` and `stele init`. Never by the hook.

```
{"protocol": 1, "op": "read", "root": "<the project, absolute>", "config": { … }}
{"protocol": 1, "fingerprint": "<1 to 256 printable bytes>", "rules": [ … ]}
```

Each rule:

| Field | |
| :-- | :-- |
| `body` | The rule, as the agent reads it. Required, at most 1 MiB, no NUL, and no line of the form `<!-- stele rule … -->`. |
| `scope` | The paths it applies to, as in `stele.md`: at most 64 patterns of 1 KiB. |
| `description`, `tags` | As in `stele.md`: a description of at most 1 KiB, at most 32 tags of 64 bytes. |
| `meta` | Other keys, as in `stele.md`: `check`, `pattern`, `message`, … At most 64; keys are lower-case letters, digits, `_` and `-`; values at most 1 KiB. |
| `file`, `line` | Where it came from, for messages: a relative path with `/`, no drive or `:` in its first part; a line from 1, or 0 for none. |
| `id` | Only if your source owns its ids: a lower-case UUID, never one another rule has. stele makes one otherwise. |

Every field but `body` is one line: no line break (`\r`, `\n`, U+0085,
U+2028, U+2029) and no NUL. At most 10,000 rules, and 4 MiB of bodies
together: every rule goes into the agents' rules files. Their order is their position: keep it stable, and
stele keeps their ids when the text changes. A `read` has 1 minute, or the
entry's `timeout`; its answer at most 32 MiB.

## `fingerprint`

```
{"protocol": 1, "op": "fingerprint", "root": "…", "config": { … }}
{"protocol": 1, "fingerprint": "…"}
```

The same fingerprint `read` would give, cheaply: it changes when the rules
do. 10 seconds, 1 MiB.

## Example: rules from a text file

`stele-source-paragraphs` reads one rule per paragraph of the file
`config.file` names. Build it with `go build -o stele-source-paragraphs .`
and put it on your `PATH`.

```go
// stele-source-paragraphs reads rules from a plain text file: one rule per
// paragraph. stele.json: {"name": "paragraphs", "config": {"file": "GUIDELINES.txt"}}
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type request struct {
	Op     string `json:"op"`
	Root   string `json:"root"`
	Config struct {
		File string `json:"file"`
	} `json:"config"`
}

type rule struct {
	Body string `json:"body"`
	File string `json:"file"`
}

func main() {
	var req request
	answer := map[string]any{"protocol": 1}
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		answer["error"] = err.Error()
	} else if req.Op == "describe" {
		answer["kind"], answer["protocols"], answer["version"] = "source", []int{1}, "0.1.0"
	} else if data, err := os.ReadFile(filepath.Join(req.Root, req.Config.File)); err != nil {
		answer["error"] = fmt.Sprintf("%s: %v", req.Config.File, err)
	} else {
		answer["fingerprint"] = fmt.Sprintf("%x", sha256.Sum256(data))
		if req.Op == "read" {
			rules := []rule{}
			for _, p := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
				if p = strings.TrimSpace(p); p != "" {
					rules = append(rules, rule{Body: p, File: req.Config.File})
				}
			}
			answer["rules"] = rules
		}
	}
	json.NewEncoder(os.Stdout).Encode(answer)
}
```

It trusts `config.file` to stay inside the project; a real plugin should
check that.

## `validate` (checker)

Sent by `stele check`, before any file is checked, with every rule that
names your checker.

```
{"protocol": 1, "op": "validate", "config": { … }, "rules": [{"id": "…", "body": "…", "scope": ["…"], "meta": {"check": "<name>", …}}]}
{"protocol": 1, "problems": [{"rule_id": "…", "message": "word: is missing"}]}
```

Report each rule whose settings, its `meta`, you can't use; `[]` if none.
stele calls such a rule invalid, fails `stele check`, and doesn't send it to
`check`. 10 seconds, 1 MiB.

## `check` (checker)

```
{"protocol": 1, "op": "check", "root": "…", "config": { … }, "files": ["main.go"], "rules": [ … ]}
{"protocol": 1, "violations": [{"rule_id": "…", "file": "main.go", "line": 4, "column": 5, "message": "…"}], "skipped": [{"file": "…", "reason": "…"}]}
```

`files` are relative, with `/`: read them under `root` yourself. stele sends
only the files some of the rules' `scope` covers, and drops a violation outside its
rule's scope. Each violation names one of the rules and one of the files
sent; `line` and `column` start at 1, the column counting characters.
`message` is optional, the rule's own text by default. List a file you
couldn't check under `skipped`: stele reports it rather than pass it. 5
minutes, or the entry's `timeout`; at most 32 MiB and 100,000 violations.

Whether a violation fails the build is the rule's `blocking:`, never yours.

## Example: a checker

`stele-checker-word` flags every line that holds a rule's `word:`.

```go
// stele-checker-word flags every line that holds a rule's word. A rule:
//
//	check: word
//	word: TODO
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type request struct {
	Op    string   `json:"op"`
	Root  string   `json:"root"`
	Files []string `json:"files"`
	Rules []struct {
		ID   string            `json:"id"`
		Meta map[string]string `json:"meta"`
	} `json:"rules"`
}

type violation struct {
	RuleID string `json:"rule_id"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func main() {
	var req request
	answer := map[string]any{"protocol": 1}
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		answer["error"] = err.Error()
	}
	switch req.Op {
	case "describe":
		answer["kind"], answer["protocols"], answer["version"] = "checker", []int{1}, "0.1.0"
	case "validate":
		problems := []map[string]string{}
		for _, r := range req.Rules {
			if r.Meta["word"] == "" {
				problems = append(problems, map[string]string{"rule_id": r.ID, "message": "word: is missing"})
			}
		}
		answer["problems"] = problems
	case "check":
		found, skipped := []violation{}, []map[string]string{}
		for _, f := range req.Files {
			file, err := os.Open(filepath.Join(req.Root, filepath.FromSlash(f)))
			if err != nil {
				skipped = append(skipped, map[string]string{"file": f, "reason": "cannot be read"})
				continue
			}
			lines := bufio.NewScanner(file)
			for n := 1; lines.Scan(); n++ {
				for _, r := range req.Rules {
					text := lines.Text()
					if i := strings.Index(text, r.Meta["word"]); i >= 0 {
						// The column counts characters, not bytes.
						found = append(found, violation{r.ID, f, n, utf8.RuneCountInString(text[:i]) + 1})
					}
				}
			}
			file.Close()
		}
		answer["violations"], answer["skipped"] = found, skipped
	}
	json.NewEncoder(os.Stdout).Encode(answer)
}
```

A file it can't open is listed under `skipped`, so stele reports it rather
than pass it.

## `push` (sink)

A **sink** plugin, `stele-sink-<name>`, receives stele's log when you run
`stele push` ([ADR-0014](adr/0014-sinks-push-and-status.md)). A project lists
it under `sinks`:

```json
{ "sinks": [{ "name": "<name>", "config": { … }, "timeout": "2m" }] }
```

```
{"protocol": 1, "op": "push", "config": { … }, "project": "0f1f82a6-…", "records": [{"v":1,"kind":"wrote",…,"mac":"…"}, …]}
{"protocol": 1, "accepted": 2}
```

Each record is one line of the project's `.stele/log.jsonl`, exactly as stele
wrote it: rule ids, file paths, session and prompt ids, and counts, never
code.

`accepted` is how many you took: the first `accepted` records, in order. stele
sends the rest again next time. Take them all, or stop at the first you can't
keep. A number below 0 or above the records sent is an error.

stele may send a record twice: when it stops between your answer and saving
how far it got. Use `mac` to drop a record you already have. At most 500
records and 4 MiB a call; 1 minute, or the entry's `timeout`. Your answer is
at most 1 MiB.

stele itself opens no connection. Where the records go is up to your
program.

## Example: a sink

`stele-sink-file` appends every record to the file its `config` names.

```go
// stele-sink-file appends the records stele push sends it to a file:
//
//	"sinks": [{"name": "file", "config": {"path": "/home/you/stele-log.jsonl"}}]
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	Op     string `json:"op"`
	Config struct {
		Path string `json:"path"`
	} `json:"config"`
	Records []json.RawMessage `json:"records"`
}

func main() {
	var req request
	answer := map[string]any{"protocol": 1}
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		answer["error"] = err.Error()
	}
	switch req.Op {
	case "describe":
		answer["kind"], answer["protocols"], answer["version"] = "sink", []int{1}, "0.1.0"
	case "push":
		f, err := os.OpenFile(req.Config.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			answer["error"] = err.Error()
			break
		}
		n := 0
		for _, r := range req.Records {
			if _, err := fmt.Fprintf(f, "%s\n", r); err != nil {
				break
			}
			n++
		}
		f.Close()
		answer["accepted"] = n
	}
	json.NewEncoder(os.Stdout).Encode(answer)
}
```

It stops at the first record it can't write, and says how many it kept, so
stele sends the rest next time.

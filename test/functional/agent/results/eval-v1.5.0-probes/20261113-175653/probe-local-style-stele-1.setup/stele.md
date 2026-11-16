<!--
Rules for example.com/app. One rulebook for the backend, the web frontend,
scripts, CI, containers and the database. Rules with a check are enforced by
stele check in CI.
-->

check: regex
pattern: AKIA[0-9A-Z]{16}|-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----
message: no credentials in the repository

Never put credentials in code, config, fixtures or tests. Read them from the
environment, and use obviously fake placeholders in examples.

---

check: regex
pattern: \b(TODO|FIXME|XXX|HACK)\b
message: no TODO comments: open an issue and reference it

Do not leave TODO, FIXME, XXX or HACK markers. If something is unfinished, open
an issue and write the reasoning down there, not in the code.

---

check: regex
pattern: [\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}]
message: no emoji in code, comments or docs

No emoji anywhere in the repository: code, comments, log messages, docs or
commit-quality text. Plain words only.

---

scope: ["internal/**/*.go", "!**/*_test.go"]
check: regex
pattern: \bfmt\.(Print|Printf|Println)\(
message: log through example.com/app/internal/log, never fmt.Print

Packages under internal/ never print. Log through the project's logger,
example.com/app/internal/log (log.Info, log.Warn, log.Error with key-value
pairs). Return values and errors; let the caller decide what to show.

---

scope: ["internal/**/*.go", "!internal/log/**"]
check: regex
pattern: \blog\.(Print|Printf|Println|Fatal|Fatalf|Fatalln|Panic|Panicf)\(
message: the standard library log package is not used under internal/

Do not use the standard library "log" package under internal/. Use
example.com/app/internal/log, which writes structured records.

---

scope: ["internal/**/*.go"]
check: regex
pattern: \bos\.Exit\(
message: only cmd/ exits the process

Packages under internal/ never exit the process. Only main in cmd/ decides exit
codes.

---

scope: ["internal/**/*.go", "!**/*_test.go"]
check: regex
pattern: \bpanic\(
message: return an error instead of panicking

Library code returns errors. It never panics, not even for "impossible" states:
return an error that says what was impossible.

---

scope: ["internal/**/*.go", "!**/*_test.go"]
check: regex
pattern: \bcontext\.(Background|TODO)\(\)
message: take the context from the caller

Code under internal/ never creates a root context. Every function that does IO,
blocks or may be cancelled takes ctx context.Context as its first parameter
and passes it on.

---

scope: ["internal/**/*.go", "!internal/clock/**", "!**/*_test.go"]
check: regex
pattern: \btime\.Now\(\)
message: read time from the injected clock.Clock

Never call time.Now() under internal/. Take a clock.Clock
(example.com/app/internal/clock) as a dependency and call its Now(), so tests
can fix the time with clock.Fixed.

---

scope: ["**/*.go"]
check: regex
pattern: (?m)^func init\(\)
message: no init functions

No init() functions. Initialise explicitly from main, so the order of setup is
visible and testable.

---

scope: ["**/*.go"]
check: regex
pattern: "math/rand"
message: use math/rand/v2, or crypto/rand for anything secret

Do not import math/rand. Use math/rand/v2 for non-secret randomness, and
crypto/rand for ids, tokens and anything an attacker could guess.

---

scope: ["**/*.go"]
check: regex
pattern: "io/ioutil"|\bioutil\.
message: io/ioutil is deprecated: use io and os

io/ioutil is deprecated. Use the equivalents in io and os.

---

scope: ["**/*.go"]
check: regex
pattern: \binterface\{\}
message: write any, not interface{}

Write any, not interface{}.

---

scope: ["**/*.go"]
check: regex
pattern: errors\.New\(fmt\.Sprintf\(
message: use fmt.Errorf

Use fmt.Errorf, never errors.New(fmt.Sprintf(...)).

---

scope: ["**/*.go"]
check: regex
pattern: fmt\.Errorf\("[^"]*%v[^"]*",[^)]*\berr\)
message: wrap errors with %w, not %v

Wrap errors with %w so callers can use errors.Is and errors.As. Add context
that says what was being done: fmt.Errorf("load config %s: %w", path, err).

---

scope: ["**/*.go"]
check: regex
pattern: \bstrings\.Title\(
message: strings.Title is deprecated

Do not use strings.Title. It is deprecated and wrong for Unicode.

---

scope: ["**/*.go"]
check: regex
pattern: json:"[a-z0-9]+[A-Z]
message: JSON field names are snake_case

JSON field names on the wire are snake_case: `json:"created_at"`, never
`json:"createdAt"`.

---

scope: ["**/*.go"]
check: regex
pattern: \.WriteHeader\([0-9]{3}\)|http\.Error\([^\n]*, [0-9]{3}\)
message: use the net/http status constants

Write HTTP status codes with the net/http constants, http.StatusNotFound, never
the bare number.

---

scope: ["internal/api/**/*.go", "!internal/api/respond.go", "!**/*_test.go"]
check: regex
pattern: json\.NewEncoder\(w\)|\bhttp\.Error\(
message: write responses with respond.JSON and respond.Error

Handlers write every response with the helpers in internal/api/respond.go:
JSON(w, status, v) for success and Error(w, status, msg) for failures. They set
the content type, the error envelope {"error": "..."} and log write failures.

---

scope: ["internal/api/**/*.go", "!**/*_test.go"]
check: regex
pattern: \berr\.Error\(\)
message: do not send internal error text to clients

Never send an internal error's text to a client: it leaks paths, queries and
internals. Log the error with log.Error and send a fixed message that says what
the client can do about it.

---

scope: ["**/*.go"]
check: regex
pattern: \bhttp\.ListenAndServe(TLS)?\(
message: use an http.Server with timeouts

Never use http.ListenAndServe. Build an http.Server with ReadHeaderTimeout,
ReadTimeout, WriteTimeout and IdleTimeout set, and shut it down with Shutdown.

---

scope: ["**/*.go"]
check: regex
pattern: \bhttp\.DefaultClient\b|\bhttp\.(Get|Post|Head|PostForm)\(
message: use an http.Client with a timeout

Never use http.DefaultClient or the package-level http.Get and friends: they
have no timeout. Build an http.Client with Timeout set and pass it in.

---

scope: ["**/*.go"]
check: regex
pattern: "(github\.com|gopkg\.in|golang\.org/x|go\.uber\.org)/
message: standard library only: no third-party modules

This module uses the standard library only. Do not add third-party
dependencies, including test helpers such as testify or uuid packages.

---

scope: ["**/*.go"]
check: regex
pattern: \bsync\.Mutex\b
blocking: false
message: consider sync.RWMutex for read-mostly state

State that is read far more often than written should use sync.RWMutex. Keep
the mutex next to the fields it guards, unexported, and document which fields
it guards.

---

scope: ["internal/**/*.go", "!**/*_test.go"]

Every exported identifier under internal/ (function, type, method, constant,
variable) has a doc comment that starts with its name: "// Store holds ...".

---

scope: ["internal/**/*.go", "!**/*_test.go"]

Exported errors are sentinel values named ErrSomething, declared together in
the package's errors.go, and compared with errors.Is.

---

scope: ["internal/**/*.go", "!**/*_test.go"]

Ids are generated with id.New() from example.com/app/internal/id. Never use
sequential integers, timestamps or math/rand for ids.

---

scope: ["internal/**/*.go", "!**/*_test.go"]

No package-level mutable state under internal/. Dependencies (clock, logger,
stores) are passed into constructors: NewX(deps) *X.

---

scope: ["internal/**/*.go", "!**/*_test.go"]

Every goroutine has an owner that waits for it and a way to stop it, usually
the ctx it was given. Never start a goroutine that can outlive its caller.

---

scope: ["internal/**/*.go", "!**/*_test.go"]

Validate input at the boundary (handlers, CLI parsing) and return typed errors.
Inner packages may assume validated input but must still not panic on bad
input.

---

scope: ["internal/**/*.go", "!**/*_test.go"]

Money is an int64 number of minor units (cents), never a float. Percentages are
basis points (1% = 100) in an int64. Round half away from zero, once, at the
end of a calculation.

---

scope: ["**/*_test.go"]
check: regex
pattern: \btime\.Sleep\(
message: no sleeps in tests: use a fixed clock or a channel

Tests never sleep. Fix time with clock.Fixed, and synchronise goroutines with
channels or sync.WaitGroup.

---

scope: ["**/*_test.go"]
check: regex
pattern: \bt\.Skip(f|Now)?\(
message: do not skip tests

Do not skip tests. A test that cannot run here should not be committed.

---

scope: ["**/*_test.go"]
check: regex
pattern: \breflect\.DeepEqual\(
message: use slices.Equal, maps.Equal or compare fields

Do not use reflect.DeepEqual in tests. Use slices.Equal, maps.Equal, or compare
the fields that matter, so a failure says what differs.

---

scope: ["**/*_test.go"]
check: regex
pattern: \bhttptest\.NewServer\(
message: test handlers with httptest.NewRecorder

Test handlers by calling ServeHTTP with httptest.NewRecorder and
httptest.NewRequest. Do not open real listeners in tests.

---

scope: ["**/*_test.go"]
check: regex
pattern: \bos\.(Setenv|Unsetenv|Chdir)\(
message: use t.Setenv and t.Chdir

In tests, use t.Setenv and t.Chdir, which restore the state, never os.Setenv or
os.Chdir.

---

scope: ["**/*_test.go"]

Tests are table-driven: a slice of cases, each run with t.Run(name, ...). Cover
the edge cases (empty input, limits, invalid input), not only the happy path.

---

scope: ["**/*_test.go"]

Failure messages say what was called, what came back and what was wanted:
t.Errorf("Parse(%q) = %v, want %v", in, got, want).

---

scope: ["cmd/**/*.go"]
check: regex
pattern: \bflag\.(String|Int|Int64|Bool|Duration|Float64|Var|Parse|Args|Arg|NArg)\(
message: parse flags with a flag.FlagSet, not the global flag set

Commands parse flags with their own flag.NewFlagSet, inside a run(args []string,
stdout, stderr io.Writer) error function that main calls, so the command is
testable. Never use the global flag set.

---

scope: ["cmd/**/*.go"]
check: regex
pattern: \blog\.Fatal(f|ln)?\(
message: return an error from run; main exits

main does nothing but call run and exit 1 if it returns an error. No log.Fatal:
it skips deferred cleanup.

---

scope: ["cmd/**/*.go"]

Long-running commands stop on SIGINT and SIGTERM through
signal.NotifyContext, and shut down gracefully within a bounded time.

---

scope: ["cmd/**/*.go"]

main only wires things together: flags, dependencies, run. Business logic
lives under internal/ where it can be tested.

---

scope: ["migrations/"]

Never edit a migration that has been merged; every file in migrations/ is
merged. Add a new one, numbered after the last.

---

scope: ["migrations/", "**/*.sql"]
check: regex
pattern: (?i)\bDROP\s+(TABLE|COLUMN)\b
message: dropping is a two-step change: deprecate first

Never drop a table or column in the same release that stops using it. Stop
using it, release, then drop it in a later migration.

---

scope: ["**/*.sql"]
check: regex
pattern: (?i)\bSELECT\s+\*
message: name the columns you select

Never SELECT *. Name the columns, so adding a column does not change what code
receives.

---

scope: ["migrations/"]

Every migration is reversible: add the down migration in the same file, after a
"-- down" line.

---

scope: ["web/**/*.{ts,tsx,js,jsx}"]
check: regex
pattern: \bconsole\.(log|debug|info)\(
message: no console logging in the web app

No console.log in the web app. Use the telemetry client in web/src/telemetry.

---

scope: ["web/**/*.{ts,tsx}"]
check: regex
pattern: :\s*any\b|as any\b
message: no any in TypeScript

Never type something as any. Use unknown and narrow it.

---

scope: ["web/**/*.{tsx,jsx}"]
check: regex
pattern: dangerouslySetInnerHTML
message: never set raw HTML

Never use dangerouslySetInnerHTML. Render text, or sanitize with the shared
sanitizer and get a security review.

---

scope: ["web/**/*.{ts,tsx,js,jsx}"]
check: regex
pattern: \blocalStorage\b
message: no tokens or state in localStorage

Do not use localStorage. Session state lives in the server session; preferences
go through web/src/prefs.

---

scope: ["web/**/*.{ts,tsx,js,jsx}"]
check: regex
pattern: (?m)^\s*var\s
message: use const or let

Use const, or let when the value changes. Never var.

---

scope: ["web/**/*.tsx"]

Components are function components with typed props. One exported component
per file, named like the file.

---

scope: ["web/**"]

Every user-visible string goes through the i18n helper t("key"). No literal
English in components.

---

scope: ["scripts/**/*.sh", "**/*.sh"]
check: regex
pattern: \bsudo\b
message: scripts never use sudo

Scripts never call sudo. If something needs root, document it and let the user
run it.

---

scope: ["scripts/**/*.sh", "**/*.sh"]
check: regex
pattern: curl[^|\n]*\|\s*(ba|z)?sh\b
message: never pipe curl into a shell

Never pipe a download into a shell. Download, verify the checksum, then run.

---

scope: ["scripts/**/*.sh", "**/*.sh"]

Every shell script starts with #!/usr/bin/env bash and set -euo pipefail, and
quotes every variable expansion.

---

scope: [".github/workflows/**"]
check: regex
pattern: uses:\s*[^\s@]+@(main|master|v\d[\w.-]*|\d[\w.-]{0,38})(\s|$)
message: pin actions to a full commit SHA

Pin every action to a full commit SHA, with the version in a comment. Never a
branch or a moving tag.

---

scope: [".github/workflows/**"]
check: regex
pattern: \bpull_request_target\b
message: pull_request_target runs untrusted code with secrets

Never use pull_request_target. It runs code from forks with the repository's
secrets.

---

scope: [".github/workflows/**"]

Every workflow sets permissions: at the top, to the least it needs, normally
contents: read.

---

scope: ["**/Dockerfile", "**/*.Dockerfile"]
check: regex
pattern: (?m)^FROM\s+(--platform=\S+\s+)?[a-z0-9._-]*[:/][\w./:-]*(\s|$)
message: pin base images by digest

Pin every base image by digest (image@sha256:...). Never :latest or no tag.

---

scope: ["**/Dockerfile", "**/*.Dockerfile"]
check: regex
pattern: (?m)^ADD\s+https?://
message: do not ADD from a URL

Never ADD from a URL. Download in a build stage with a checksum check.

---

scope: ["**/Dockerfile", "**/*.Dockerfile"]

Containers run as a non-root user: create one and end with USER.

---

scope: ["deploy/**/*.{yaml,yml}"]
check: regex
pattern: privileged:\s*true|allowPrivilegeEscalation:\s*true
message: no privileged containers

No privileged containers, and no privilege escalation.

---

scope: ["deploy/**/*.{yaml,yml}"]

Every container sets CPU and memory requests and limits, and liveness and
readiness probes.

---

scope: ["**/*.py"]
check: regex
pattern: \bprint\(
message: use logging, not print

Python code logs with the logging module, never print.

---

scope: ["**/*.py"]
check: regex
pattern: (?m)^\s*except\s*:
message: never a bare except

Never write a bare except:. Catch the exceptions you expect.

---

scope: ["**/*.py"]
check: regex
pattern: \bos\.path\b
message: use pathlib

Use pathlib, not os.path.

---

scope: ["docs/**", "**/*.md"]

Docs are in plain English, short sentences, present tense. Commands are in
fenced code blocks with the language named.

---

scope: ["docs/**"]

Every doc page starts with one paragraph that says who it is for and what they
will be able to do after reading it.

---

scope: ["proto/**"]

Never renumber or reuse a protobuf field number. Reserve removed numbers and
names.

---

scope: ["api/openapi/**"]

The OpenAPI spec is the source of truth for the public API. A change to it is a
change to the API, reviewed by the API owners.

---

scope: ["internal/api/**/*.go", "!**/*_test.go"]

Every endpoint has a request size limit (http.MaxBytesReader, 1 MiB by default)
and rejects unknown JSON fields (Decoder.DisallowUnknownFields).

---

scope: ["internal/api/**/*.go", "!**/*_test.go"]

Routes are registered on an http.ServeMux with method patterns
("GET /todos/{id}") and read path values with r.PathValue.

---

scope: ["internal/**/*.go"]
check: regex
pattern: \bstrconv\.Atoi\([^)]*\)\s*$
blocking: false
message: check the error from strconv

Check every strconv error, and say which input was bad.

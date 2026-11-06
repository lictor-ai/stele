// Package plugintest is a fake plugin for tests. A test binary whose TestMain
// sees it was started as stele-<kind>-<mode> calls Serve, which answers one
// request the way mode says. Tests install the test binary itself under such
// a name: no plugin is built, on any OS. It starts nothing; only tests import
// it.
package plugintest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Is reports whether exe, a program's path, names a fake plugin, and so
// whether TestMain should call Serve.
func Is(exe string) bool {
	_, _, ok := parse(exe)
	return ok
}

// parse reads the kind and the mode from exe's name: stele-<kind>-<mode>.
func parse(exe string) (kind, mode string, ok bool) {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe")
	rest, ok := strings.CutPrefix(base, "stele-")
	if !ok {
		return "", "", false
	}
	kind, mode, ok = strings.Cut(rest, "-")
	switch kind {
	case "source", "checker", "sink":
		return kind, mode, ok && mode != ""
	}
	return "", "", false
}

// The modes Serve knows:
//
//	good      describes itself, then answers from the request's config
//	witness   as good, after appending "<kind> <op>" to $STELE_FAKE_WITNESS
//	env       as good, and every answer also holds "path" ($PATH) and "cwd"
//	errors    describes itself, then answers {"error": …}
//	badproto  answers in protocol 2 with an error
//	badjson   answers with text that is not JSON
//	trailing  answers, then writes more
//	wrongkind describes itself as another kind
//	crash     writes to stderr and exits 3
//	slow      sleeps 30 s, then answers
//	huge      answers with 2 MiB of JSON
//	noisy     writes 300 lines with escapes to stderr, then answers
//
// config, in a good answer: "rules", "fingerprint", "problems",
// "violations", "skipped" are copied into the answer for the ops that have
// them.

// Serve answers the one request on stdin as exe's mode says, and returns the
// exit status.
func Serve(exe string, stdin io.Reader, stdout, stderr io.Writer) int {
	kind, mode, _ := parse(exe)
	var req map[string]json.RawMessage
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		fmt.Fprintln(stderr, "fake plugin: bad request:", err)
		return 2
	}
	var op string
	_ = json.Unmarshal(req["op"], &op)
	var config map[string]json.RawMessage
	_ = json.Unmarshal(req["config"], &config)

	answer := map[string]any{"protocol": 1}
	describe := func(k string) {
		answer["kind"], answer["protocols"], answer["version"] = k, []int{1}, "1.0.0"
	}
	good := func() {
		if op == "describe" {
			describe(kind)
			return
		}
		fields := map[string][]string{
			"read":        {"rules", "fingerprint"},
			"fingerprint": {"fingerprint"},
			"validate":    {"problems"},
			"check":       {"violations", "skipped"},
		}
		defaults := map[string]any{"rules": []any{}, "fingerprint": "fp", "problems": []any{}, "violations": []any{}}
		for _, f := range fields[op] {
			if v, ok := config[f]; ok {
				answer[f] = v
			} else if d, ok := defaults[f]; ok {
				answer[f] = d
			}
		}
		if op == "push" {
			var recs []json.RawMessage
			_ = json.Unmarshal(req["records"], &recs)
			answer["accepted"] = len(recs)
		}
	}

	switch mode {
	case "good":
		good()
	case "witness":
		if f, err := os.OpenFile(os.Getenv("STELE_FAKE_WITNESS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s\n", kind, op)
			f.Close()
		}
		good()
	case "env":
		good()
		wd, _ := os.Getwd()
		answer["path"], answer["cwd"] = os.Getenv("PATH"), wd
	case "errors":
		if op == "describe" {
			describe(kind)
		} else {
			answer["error"] = "the fake plugin was told to fail"
		}
	case "badproto":
		answer = map[string]any{"protocol": 2, "error": "unsupported protocol 1; speaks [2]"}
	case "badjson":
		fmt.Fprint(stdout, "this is not JSON")
		return 0
	case "trailing":
		good()
		data, _ := json.Marshal(answer)
		fmt.Fprintf(stdout, "%s {}", data)
		return 0
	case "wrongkind":
		if kind == "source" {
			describe("checker")
		} else {
			describe("source")
		}
	case "crash":
		fmt.Fprintln(stderr, "the fake plugin crashed")
		return 3
	case "slow":
		time.Sleep(30 * time.Second)
		good()
	case "huge":
		answer["padding"] = strings.Repeat("x", 2<<20)
		good()
	case "noisy":
		for i := 0; i < 300; i++ {
			fmt.Fprintf(stderr, "\x1b[31mline %d\x1b[0m\n", i)
		}
		good()
	default:
		fmt.Fprintln(stderr, "fake plugin: unknown mode", mode)
		return 2
	}
	data, err := json.Marshal(answer)
	if err != nil {
		fmt.Fprintln(stderr, "fake plugin:", err)
		return 2
	}
	stdout.Write(data)
	return 0
}

// Package extchecktest is a fake scanner for the checker plugins' tests. A
// test binary whose TestMain sees it was started under the scanner's name
// calls Scan, which records how it was run and prints a recorded output.
// Tests install the test binary itself under that name: no scanner is
// built, on any OS, and the real one never runs. Only tests import it.
package extchecktest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Is reports whether exe, a program's path, is the fake scanner name.
func Is(exe, name string) bool {
	return strings.TrimSuffix(filepath.Base(exe), ".exe") == name
}

// Scan is the fake scanner. It appends its arguments, environment and
// working directory, as one JSON line, to $FAKE_SCANNER_LOG; prints the
// file $FAKE_SCANNER_OUT, if set, with {{DIR}} made $FAKE_SCANNER_DIR as
// it would be in JSON; writes $FAKE_SCANNER_STDERR on stderr; and returns
// $FAKE_SCANNER_EXIT, the exit status.
func Scan(args []string, stdout, stderr io.Writer) int {
	if log := os.Getenv("FAKE_SCANNER_LOG"); log != "" {
		wd, _ := os.Getwd()
		data, _ := json.Marshal(Call{Args: args, Env: os.Environ(), Wd: wd})
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s\n", data)
			f.Close()
		}
	}
	if out := os.Getenv("FAKE_SCANNER_OUT"); out != "" {
		data, err := os.ReadFile(out)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 99
		}
		dir, _ := json.Marshal(os.Getenv("FAKE_SCANNER_DIR"))
		stdout.Write(bytes.ReplaceAll(data, []byte("{{DIR}}"), dir[1:len(dir)-1]))
	}
	fmt.Fprint(stderr, os.Getenv("FAKE_SCANNER_STDERR"))
	code, _ := strconv.Atoi(os.Getenv("FAKE_SCANNER_EXIT"))
	return code
}

// Call is one run of the fake scanner.
type Call struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
	Wd   string   `json:"wd"`
}

// Install puts this test binary in dir as the scanner name, and makes dir
// the whole PATH.
func Install(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// Fake installs the fake scanner name on its own on PATH, to print the
// testdata file out ("" for nothing) for the project at dir, exit with
// exit and write stderr. It returns the log of its calls, for Calls.
func Fake(t *testing.T, name, dir, out string, exit int, stderr string) string {
	t.Helper()
	Install(t, t.TempDir(), name)
	abs := ""
	if out != "" {
		var err error
		if abs, err = filepath.Abs(filepath.Join("testdata", out)); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("FAKE_SCANNER_OUT", abs)
	t.Setenv("FAKE_SCANNER_DIR", dir)
	t.Setenv("FAKE_SCANNER_EXIT", strconv.Itoa(exit))
	t.Setenv("FAKE_SCANNER_STDERR", stderr)
	t.Setenv("FAKE_SCANNER_LOG", log)
	return log
}

// Calls returns each run of the fake scanner logged in log.
func Calls(t *testing.T, log string) []Call {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	var cs []Call
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var c Call
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			t.Fatal(err)
		}
		cs = append(cs, c)
	}
	return cs
}

// HasEnv reports whether c ran with kv in its environment.
func (c Call) HasEnv(kv string) bool {
	for _, e := range c.Env {
		if e == kv {
			return true
		}
	}
	return false
}

package publish

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMain makes every test hermetic: git reads a global config made here
// and no system config, so Git for Windows' credential manager and the
// user's own settings never run; no token is in the environment.
//
// With STELE_FAKE_CLI=1 the test binary is not a test run but a fake gh,
// glab, git or browser opener: see fakeMain.
func TestMain(m *testing.M) {
	if os.Getenv("STELE_FAKE_CLI") == "1" {
		os.Exit(fakeMain())
	}
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "stele-publish-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	global := filepath.Join(dir, "gitconfig")
	config := "[user]\n\tname = t\n\temail = t@example.com\n[commit]\n\tgpgsign = false\n[core]\n\tautocrlf = false\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(global, []byte(config), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Setenv("GIT_CONFIG_GLOBAL", global)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range tokenEnv {
		os.Unsetenv(name)
	}
	return m.Run()
}

// fakeCall is what a fake program records of one call.
type fakeCall struct {
	Name  string            `json:"name"`
	Args  []string          `json:"args"`
	Dir   string            `json:"dir"`
	Stdin string            `json:"stdin"`
	Env   map[string]string `json:"env"`
}

// fakeMain is the fake program. It appends one JSON line describing its call
// to $FAKE_LOG, prints $FAKE_<NAME>_OUT and exits with $FAKE_<NAME>_EXIT,
// where NAME is its file name without extension, upper-cased.
func fakeMain() int {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	stdin, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	c := fakeCall{Name: name, Args: os.Args[1:], Dir: dir, Stdin: string(stdin), Env: map[string]string{}}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		c.Env[strings.ToUpper(k)] = v
	}
	if log := os.Getenv("FAKE_LOG"); log != "" {
		data, _ := json.Marshal(c)
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Write(append(data, '\n'))
			f.Close()
		}
	}
	fmt.Fprint(os.Stdout, os.Getenv("FAKE_"+upper+"_OUT"))
	code, _ := strconv.Atoi(os.Getenv("FAKE_" + upper + "_EXIT"))
	return code
}

// fakeTools puts fake programs with names in a new directory, puts that
// directory alone on PATH, and returns the log file their calls go to.
func fakeTools(t *testing.T, names ...string) (dir, log string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	for _, name := range names {
		if filepath.Ext(self) != "" {
			name += filepath.Ext(self)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log = filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("PATH", dir)
	t.Setenv("STELE_FAKE_CLI", "1")
	t.Setenv("FAKE_LOG", log)
	return dir, log
}

// fakeCalls reads the calls the fake programs recorded.
func fakeCalls(t *testing.T, log string) []fakeCall {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []fakeCall
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var c fakeCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

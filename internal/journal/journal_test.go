package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
)

var (
	testKey = sign.Key(bytes.Repeat([]byte{7}, 32))
	ts      = time.Date(2026, 11, 3, 10, 0, 0, 123e6, time.UTC)
)

// project returns a root with a .stele directory, as the hook leaves it.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, index.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func head(session string) Header {
	return Header{Project: "p1", TS: ts, Session: session, Agent: "claude-code"}
}

// lines reads the log's lines back, MAC and all.
func lines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, index.Dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// body strips a line's MAC, leaving the record as written.
func body(t *testing.T, line string) string {
	t.Helper()
	i := strings.LastIndex(line, `,"mac":"`)
	if i < 0 {
		t.Fatalf("line has no mac: %s", line)
	}
	return line[:i] + "}"
}

func TestAppendLineShape(t *testing.T) {
	sub := head("s1")
	sub.Subagent = "a1"
	tests := []struct {
		name string
		rec  Record
		want string
	}{
		{"loaded", &LoadedRecord{Header: head("s1"), Rules: []string{"r2", "r1"}},
			`{"v":1,"kind":"loaded","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"s1","agent":"claude-code","rules":["r1","r2"]}`},
		{"loaded nothing", &LoadedRecord{Header: sub},
			`{"v":1,"kind":"loaded","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"s1","agent":"claude-code","subagent":"a1","rules":[]}`},
		{"denied", &DeniedRecord{Header: head("s1"), Prompt: "u1", Via: "write", Files: []string{"a.go"}, Rules: []string{"r2", "r1"}, More: 1},
			`{"v":1,"kind":"denied","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"s1","agent":"claude-code","prompt":"u1","via":"write","files":["a.go"],"rules":["r1","r2"],"more":1}`},
		{"checked", &CheckedRecord{Header: head("s1"), File: "a.go", Rules: []string{"r1"},
			Violations: []Violation{{Rule: "r1", Line: 3, Blocking: true}}},
			`{"v":1,"kind":"checked","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"s1","agent":"claude-code","file":"a.go","rules":["r1"],"violations":[{"rule":"r1","line":3,"blocking":true}],"more":0}`},
		{"checked clean", &CheckedRecord{Header: head("s1"), File: "a.go", Rules: []string{"r1"}},
			`{"v":1,"kind":"checked","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"s1","agent":"claude-code","file":"a.go","rules":["r1"],"violations":[],"more":0}`},
		{"check run", &CheckRunRecord{Header: Header{Project: "p1", TS: ts}, Mode: "changed", Files: 4, Rules: 3, Errors: 2, Warnings: 1, Exit: 1,
			Broken: map[string]int{"r2": 1, "r1": 2}},
			`{"v":1,"kind":"check","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"","agent":"","mode":"changed","file_count":4,"rule_count":3,"problems":0,"errors":2,"warnings":1,"skipped":0,"truncated":0,"exit":1,"broken":{"r1":2,"r2":1}}`},
		{"check run clean", &CheckRunRecord{Header: Header{Project: "p1", TS: ts}, Mode: "all"},
			`{"v":1,"kind":"check","project":"p1","ts":"2026-11-03T10:00:00.123Z","session":"","agent":"","mode":"all","file_count":0,"rule_count":0,"problems":0,"errors":0,"warnings":0,"skipped":0,"truncated":0,"exit":0,"broken":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t)
			if err := Append(root, testKey, tt.rec); err != nil {
				t.Fatalf("Append: %v", err)
			}
			ls := lines(t, root)
			if len(ls) != 1 {
				t.Fatalf("%d lines, want 1", len(ls))
			}
			if diff := cmp.Diff(tt.want, body(t, ls[0])); diff != "" {
				t.Errorf("line (-want +got):\n%s", diff)
			}
			if !json.Valid([]byte(ls[0])) {
				t.Errorf("the line with its mac is not JSON: %s", ls[0])
			}
		})
	}
}

// The hook logs only where stele already made .stele/; stele check in CI
// must not create it.
func TestAppendNeedsSteleDir(t *testing.T) {
	root := t.TempDir()
	if err := Append(root, testKey, &LoadedRecord{Header: head("s1")}); err == nil {
		t.Error("Append wrote with no .stele directory")
	}
	if _, err := os.Lstat(filepath.Join(root, index.Dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Append created .stele")
	}
	// A file named .stele is no directory either.
	if err := os.WriteFile(filepath.Join(root, index.Dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, testKey, &LoadedRecord{Header: head("s1")}); err == nil {
		t.Error("Append wrote under a file named .stele")
	}
}

func TestAppendNilKey(t *testing.T) {
	root := project(t)
	if err := Append(root, nil, &LoadedRecord{Header: head("s1")}); err == nil {
		t.Error("Append wrote a line it could not sign")
	}
	if _, err := os.Lstat(filepath.Join(root, index.Dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Append created the log")
	}
}

// A path with a newline in it cannot forge a second line.
func TestAppendEscapesNewline(t *testing.T) {
	root := project(t)
	if err := Append(root, testKey, &CheckedRecord{Header: head("s1"), File: "a\n{\"v\":1}.go"}); err != nil {
		t.Fatal(err)
	}
	if ls := lines(t, root); len(ls) != 1 {
		t.Errorf("%d lines, want 1: %q", len(ls), ls)
	}
}

type got struct {
	Kind Kind
	Body string
}

func readAll(t *testing.T, root string, key sign.Key) ([]got, int) {
	t.Helper()
	var out []got
	bad, err := Read(root, key, func(k Kind, body []byte) error {
		out = append(out, got{k, string(body)})
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return out, bad
}

// Only lines this machine signed are read: a line a clone committed, one
// signed elsewhere, one edited and one torn are all skipped and counted.
func TestReadSkipsUnverified(t *testing.T) {
	root := project(t)
	if err := Append(root, testKey, &LoadedRecord{Header: head("s1"), Rules: []string{"r1"}}); err != nil {
		t.Fatal(err)
	}
	good := lines(t, root)[0]
	other := sign.Key(bytes.Repeat([]byte{8}, 32))
	otherRoot := project(t)
	if err := Append(otherRoot, other, &LoadedRecord{Header: head("s2")}); err != nil {
		t.Fatal(err)
	}
	foreign := lines(t, otherRoot)[0]
	forged := `{"v":1,"kind":"loaded","project":"p1","ts":"2026-11-03T10:00:00Z","session":"s9","agent":"claude-code","rules":[]}`
	edited := strings.Replace(good, `"s1"`, `"s9"`, 1)
	torn := good[:len(good)/2]
	data := strings.Join([]string{forged, foreign, good, edited, "", torn}, "\n")
	if err := os.WriteFile(filepath.Join(root, index.Dir, FileName), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	out, bad := readAll(t, root, testKey)
	if len(out) != 1 || out[0].Kind != Loaded || !strings.Contains(out[0].Body, `"session":"s1"`) {
		t.Errorf("read %+v, want only the good line", out)
	}
	if strings.Contains(out[0].Body, "mac") {
		t.Errorf("body still holds its mac: %s", out[0].Body)
	}
	if bad != 4 {
		t.Errorf("bad = %d, want 4 (forged, foreign, edited, torn)", bad)
	}
	if _, bad := readAll(t, root, nil); bad != 5 {
		t.Errorf("with no key, bad = %d, want every line refused", bad)
	}
}

func TestReadMissing(t *testing.T) {
	out, bad := readAll(t, project(t), testKey)
	if len(out) != 0 || bad != 0 {
		t.Errorf("an empty project read %v, %d bad", out, bad)
	}
}

// The rotated file is read first: lines come back oldest first.
func TestReadAcrossRotation(t *testing.T) {
	root := project(t)
	if err := Append(root, testKey, &LoadedRecord{Header: head("old")}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, index.Dir)
	if err := os.Rename(filepath.Join(dir, FileName), filepath.Join(dir, Rotated)); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, testKey, &LoadedRecord{Header: head("new")}); err != nil {
		t.Fatal(err)
	}
	out, bad := readAll(t, root, testKey)
	if bad != 0 || len(out) != 2 || !strings.Contains(out[0].Body, `"old"`) || !strings.Contains(out[1].Body, `"new"`) {
		t.Errorf("read %+v, %d bad; want old then new", out, bad)
	}
}

// Read takes the writers' lock, so it never sees a rotation half done or a
// line half written.
func TestReadWaitsForTheLock(t *testing.T) {
	root := project(t)
	if err := Append(root, testKey, &LoadedRecord{Header: head("s1")}); err != nil {
		t.Fatal(err)
	}
	unlock, err := safefile.Lock(filepath.Join(root, index.Dir, FileName+".lock"), time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int)
	go func() {
		Read(root, testKey, func(Kind, []byte) error { return nil })
		done <- 1
	}()
	select {
	case <-done:
		t.Fatal("Read did not wait for the lock")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Read did not finish once the lock was free")
	}
}

// A file too large to read is skipped and reported, and the other is still
// read.
func TestReadSkipsOversizedFile(t *testing.T) {
	old := maxRead
	maxRead = 512
	t.Cleanup(func() { maxRead = old })
	root := project(t)
	dir := filepath.Join(root, index.Dir)
	if err := os.WriteFile(filepath.Join(dir, Rotated), bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, testKey, &LoadedRecord{Header: Header{Session: "s"}}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, FileName)); fi.Size() > maxRead {
		t.Fatalf("the test line is %d bytes, over the test cap", fi.Size())
	}
	out, bad := readAll(t, root, testKey)
	if len(out) != 1 || bad != 1 {
		t.Errorf("read %d lines, %d bad; want the log read and the oversized file counted", len(out), bad)
	}
}

func TestReadNoSteleDir(t *testing.T) {
	if out, bad := readAll(t, t.TempDir(), testKey); len(out) != 0 || bad != 0 {
		t.Errorf("read %v, %d bad", out, bad)
	}
}

func TestReadStopsOnCallbackError(t *testing.T) {
	root := project(t)
	for range 2 {
		if err := Append(root, testKey, &LoadedRecord{Header: head("s1")}); err != nil {
			t.Fatal(err)
		}
	}
	stop := errors.New("stop")
	n := 0
	_, err := Read(root, testKey, func(Kind, []byte) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Errorf("Read = %v after %d calls, want stop after 1", err, n)
	}
}

// Two hooks logging at once leave every line whole and signed.
func TestAppendConcurrentSigned(t *testing.T) {
	root := project(t)
	const writers, each = 10, 20
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				r := &DeniedRecord{Header: head(fmt.Sprintf("s%d", w)), Via: "write", Files: []string{fmt.Sprintf("f%d.go", i)}}
				// The lock wait is short, as on the hook path: a writer that
				// gives up drops its line, which is allowed.
				_ = Append(root, testKey, r)
			}
		}()
	}
	wg.Wait()
	out, bad := readAll(t, root, testKey)
	if bad != 0 {
		t.Errorf("%d lines failed to verify: some were torn", bad)
	}
	if len(out) == 0 {
		t.Error("no line was written")
	}
}

// The log holds rule ids, paths and counts, never code: a field outside this
// list is a decision for ADR-0010, not a quiet addition.
func TestRecordFieldsAreIdsPathsCounts(t *testing.T) {
	allowed := []string{
		"v", "kind", "project", "ts", "session", "agent", "subagent",
		"rules", "prompt", "via", "files", "more", "file", "violations",
		"rule", "line", "blocking",
		"mode", "file_count", "rule_count", "problems", "errors", "warnings", "skipped", "truncated", "exit", "broken",
	}
	var tags []string
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Anonymous {
				walk(f.Type)
				continue
			}
			tags = append(tags, strings.Split(f.Tag.Get("json"), ",")[0])
			if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Struct {
				walk(f.Type.Elem())
			}
		}
	}
	for _, r := range []any{LoadedRecord{}, DeniedRecord{}, CheckedRecord{}, CheckRunRecord{}} {
		walk(reflect.TypeOf(r))
	}
	sort.Strings(allowed)
	for _, tag := range tags {
		if i := sort.SearchStrings(allowed, tag); i == len(allowed) || allowed[i] != tag {
			t.Errorf("field %q is not an id, a path or a count on the list", tag)
		}
	}
}

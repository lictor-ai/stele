package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/sign"
)

var (
	testKey = sign.Key(bytes.Repeat([]byte{9}, 32))
	at      = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
)

func now() time.Time { return at }

func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, index.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func log(t *testing.T, root, project string, sessions ...string) {
	t.Helper()
	for _, s := range sessions {
		r := &journal.WroteRecord{Header: journal.Header{Project: project, TS: at, Session: s, Agent: "claude-code"}, File: "a.go"}
		if err := journal.Append(root, testKey, r); err != nil {
			t.Fatal(err)
		}
	}
}

// fake is a sink that keeps what it accepts.
type fake struct {
	name   string
	accept int // per call; 0 is all
	err    error
	got    []json.RawMessage
	calls  int
}

func (f *fake) target() Target {
	return Target{Name: f.name, Push: func(_ context.Context, project string, recs []json.RawMessage) (int, error) {
		f.calls++
		if f.err != nil {
			return 0, f.err
		}
		n := len(recs)
		if f.accept > 0 && f.accept < n {
			n = f.accept
		}
		f.got = append(f.got, recs[:n]...)
		return n, nil
	}}
}

func (f *fake) sessions(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range f.got {
		var h struct {
			Session string `json:"session"`
			MAC     string `json:"mac"`
		}
		if err := json.Unmarshal(r, &h); err != nil || h.MAC == "" {
			t.Fatalf("record %s: %v (mac %q)", r, err, h.MAC)
		}
		out = append(out, h.Session)
	}
	return out
}

func run(t *testing.T, root string, fs ...*fake) Result {
	t.Helper()
	var ts []Target
	for _, f := range fs {
		ts = append(ts, f.target())
	}
	res, err := Run(context.Background(), root, testKey, "p1", ts, now)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPushNeverSendsTwice(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b")
	f := &fake{name: "s"}
	if res := run(t, root, f); res.Sinks[0].Sent != 2 || res.Sinks[0].Err != nil {
		t.Fatalf("first push = %+v", res.Sinks[0])
	}
	if res := run(t, root, f); res.Sinks[0].Sent != 0 {
		t.Errorf("second push sent %d", res.Sinks[0].Sent)
	}
	log(t, root, "p1", "c")
	run(t, root, f)
	if got := f.sessions(t); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("sink got %v, want a,b,c", got)
	}
}

func TestRecordsAreLogLinesVerbatim(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	f := &fake{name: "s"}
	run(t, root, f)
	data, _ := os.ReadFile(filepath.Join(root, index.Dir, journal.FileName))
	if string(f.got[0])+"\n" != string(data) {
		t.Errorf("record %s\nis not the log line %s", f.got[0], data)
	}
}

func TestPushPartialAdvancesByAccepted(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b", "c")
	f := &fake{name: "s", accept: 2}
	res := run(t, root, f)
	if s := res.Sinks[0]; s.Sent != 2 || !s.Partial || s.Pending != 1 {
		t.Fatalf("partial push = %+v", s)
	}
	f.accept = 0
	run(t, root, f)
	if got := f.sessions(t); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("sink got %v, want a,b,c once each", got)
	}
}

func TestPushFailureKeepsCursor(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	f := &fake{name: "s", err: errors.New("down")}
	if res := run(t, root, f); res.Sinks[0].Err == nil || res.Sinks[0].Sent != 0 {
		t.Fatalf("failed push = %+v", res.Sinks[0])
	}
	f.err = nil
	run(t, root, f)
	if got := f.sessions(t); strings.Join(got, ",") != "a" {
		t.Errorf("sink got %v, want a", got)
	}
}

func TestPushOneSinkFailsOthersRun(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	bad, good := &fake{name: "bad", err: errors.New("down")}, &fake{name: "good"}
	res := run(t, root, bad, good)
	if res.Sinks[0].Err == nil || res.Sinks[1].Err != nil || res.Sinks[1].Sent != 1 {
		t.Errorf("results = %+v", res.Sinks)
	}
}

func TestPushBatchesByCount(t *testing.T) {
	root := project(t)
	old := maxBatch
	maxBatch = 2
	t.Cleanup(func() { maxBatch = old })
	log(t, root, "p1", "a", "b", "c", "d", "e")
	f := &fake{name: "s"}
	run(t, root, f)
	if f.calls != 3 || len(f.got) != 5 {
		t.Errorf("%d calls, %d records; want 3 calls, 5 records", f.calls, len(f.got))
	}
}

func TestPushBadCursorIsAnError(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	f := &fake{name: "s"}
	run(t, root, f)
	path := filepath.Join(root, index.Dir, "push", "s.json")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(data, []byte(`"pushed":1`), []byte(`"pushed":0`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	log(t, root, "p1", "b")
	res := run(t, root, f)
	if !errors.Is(res.Sinks[0].Err, ErrCursor) || res.Sinks[0].Sent != 0 {
		t.Errorf("result = %+v, want ErrCursor and nothing sent", res.Sinks[0])
	}
}

func TestPushSkipsOtherProject(t *testing.T) {
	root := project(t)
	log(t, root, "other", "x")
	log(t, root, "p1", "a")
	f := &fake{name: "s"}
	res := run(t, root, f)
	if s := res.Sinks[0]; s.Sent != 1 || s.OtherProject != 1 {
		t.Errorf("result = %+v", s)
	}
}

func TestPushNoLog(t *testing.T) {
	f := &fake{name: "s"}
	res := run(t, t.TempDir(), f)
	if res.Sinks[0].Sent != 0 || f.calls != 0 || res.Sinks[0].Err != nil {
		t.Errorf("result = %+v, %d calls", res.Sinks[0], f.calls)
	}
}

func TestPushLockBusy(t *testing.T) {
	root := project(t)
	old := lockWait
	lockWait = 50 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	if err := os.WriteFile(filepath.Join(root, index.Dir, LockName), []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), root, testKey, "p1", []Target{(&fake{name: "s"}).target()}, now); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
}

func TestPending(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b")
	if n, last, err := Pending(root, testKey, "p1", "s"); n != 2 || !last.IsZero() || err != nil {
		t.Errorf("before = %d, %v, %v", n, last, err)
	}
	run(t, root, &fake{name: "s", accept: 1})
	if n, last, err := Pending(root, testKey, "p1", "s"); n != 1 || !last.Equal(at) || err != nil {
		t.Errorf("after = %d, %v, %v", n, last, err)
	}
}

func TestOne(t *testing.T) {
	f := &fake{name: "s"}
	rec := json.RawMessage(`{"v":1,"kind":"check"}`)
	res := One(context.Background(), []Target{f.target()}, "p1", rec)
	if res.Sinks[0].Sent != 1 || !reflect.DeepEqual(f.got, []json.RawMessage{rec}) {
		t.Errorf("result = %+v, got %s", res.Sinks[0], f.got)
	}
}

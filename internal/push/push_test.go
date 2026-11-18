package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
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
	defer_ string // when set, the sink defers, for this reason
	got    []json.RawMessage
	calls  int
}

func (f *fake) target() Target {
	return Target{Name: f.name, Push: func(_ context.Context, project string, recs []json.RawMessage) (Answer, error) {
		f.calls++
		if f.err != nil {
			return Answer{}, f.err
		}
		if f.defer_ != "" {
			return Answer{Deferred: true, Reason: f.defer_}, nil
		}
		n := len(recs)
		if f.accept > 0 && f.accept < n {
			n = f.accept
		}
		f.got = append(f.got, recs[:n]...)
		return Answer{Accepted: n}, nil
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

// The lock is refreshed before each sink call, so a call that outlasts the
// stale time is not taken over by a second push.
func TestPushRefreshesTheLock(t *testing.T) {
	root := project(t)
	old := maxBatch
	maxBatch = 1
	t.Cleanup(func() { maxBatch = old })
	log(t, root, "p1", "a", "b")
	lockPath := filepath.Join(root, index.Dir, LockName)
	long := time.Now().Add(-time.Hour)
	calls := 0
	target := Target{Name: "s", Push: func(context.Context, string, []json.RawMessage) (Answer, error) {
		calls++
		info, err := os.Stat(lockPath)
		if err != nil {
			t.Fatalf("no lock during a call: %v", err)
		}
		if time.Since(info.ModTime()) > time.Minute {
			t.Errorf("call %d: the lock is %v old: not refreshed", calls, time.Since(info.ModTime()))
		}
		// The call takes "an hour".
		os.Chtimes(lockPath, long, long)
		return Answer{Accepted: 1}, nil
	}}
	if _, err := Run(context.Background(), root, testKey, "p1", []Target{target}, now); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("%d calls, want 2", calls)
	}
}

// A cursor never moves back: one further on was written by another push.
func TestCursorNeverMovesBack(t *testing.T) {
	root := project(t)
	ahead := &cursor{V: cursorVersion, Sink: "s", Project: "p1", Pos: journal.Pos{File: "f", Offset: 100, Last: "x"}, Pushed: 5}
	if err := saveCursor(root, testKey, ahead); err != nil {
		t.Fatal(err)
	}
	behind := *ahead
	behind.Pos.Offset, behind.Pushed = 50, 3
	if err := saveCursor(root, testKey, &behind); !errors.Is(err, ErrCursor) {
		t.Errorf("err = %v, want ErrCursor", err)
	}
	if c, _ := loadCursor(root, testKey, "s", "p1"); c.Pushed != 5 {
		t.Errorf("cursor = %+v, moved back", c)
	}
}

func TestCursorOfAnotherProject(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	run(t, root, &fake{name: "s"})
	f := &fake{name: "s"}
	res, err := Run(context.Background(), root, testKey, "p2", []Target{f.target()}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(res.Sinks[0].Err, ErrCursor) || !strings.Contains(res.Sinks[0].Err.Error(), "p1") || f.calls != 0 {
		t.Errorf("result = %+v, %d calls", res.Sinks[0], f.calls)
	}
}

// After two rotations with nothing new, the gap is said once.
func TestPushGapSaidOnce(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	run(t, root, &fake{name: "s"})
	dir := filepath.Join(root, index.Dir)
	if err := os.Remove(filepath.Join(dir, journal.FileName)); err != nil {
		t.Fatal(err)
	}
	if res := run(t, root, &fake{name: "s"}); !res.Sinks[0].Gap {
		t.Fatalf("first push after the log went = %+v, want a gap", res.Sinks[0])
	}
	if res := run(t, root, &fake{name: "s"}); res.Sinks[0].Gap {
		t.Errorf("second push = %+v, the gap said again", res.Sinks[0])
	}
	if c, _ := loadCursor(root, testKey, "s", "p1"); !c.TS.Equal(at) {
		t.Errorf("cursor TS = %v: changed with nothing sent", c.TS)
	}
}

func TestPending(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b")
	if w, err := Pending(root, testKey, "p1", "s"); w.Lines != 2 || !w.LastPush.IsZero() || w.Deferred || err != nil {
		t.Errorf("before = %+v, %v", w, err)
	}
	run(t, root, &fake{name: "s", accept: 1})
	if w, err := Pending(root, testKey, "p1", "s"); w.Lines != 1 || !w.LastPush.Equal(at) || w.Deferred || err != nil {
		t.Errorf("after = %+v, %v", w, err)
	}
}

// A sink that defers takes nothing and is not partial: the cursor stays, its
// reason is kept for stele status, and a later push sends the lines and
// forgets the reason (ADR-0021).
func TestPushDeferredKeepsLinesForLater(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b", "c")
	off := &fake{name: "s", defer_: "sending is off in this folder"}
	res := run(t, root, off)
	if s := res.Sinks[0]; !s.Deferred || s.Reason != "sending is off in this folder" || s.Sent != 0 || s.Pending != 3 || s.Partial || s.Err != nil {
		t.Fatalf("deferred push = %+v", s)
	}
	w, err := Pending(root, testKey, "p1", "s")
	if err != nil || w.Lines != 3 || !w.Deferred || w.Reason != "sending is off in this folder" || !w.LastPush.IsZero() {
		t.Errorf("pending after a deferral = %+v, %v", w, err)
	}
	// Deferred again: still nothing moved.
	if res := run(t, root, off); res.Sinks[0].Pending != 3 || off.calls != 2 {
		t.Errorf("second deferral = %+v, %d calls", res.Sinks[0], off.calls)
	}
	on := &fake{name: "s"}
	if res := run(t, root, on); res.Sinks[0].Sent != 3 || res.Sinks[0].Deferred {
		t.Fatalf("push once on = %+v", res.Sinks[0])
	}
	if got := on.sessions(t); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("sink got %v, want a,b,c", got)
	}
	if w, err := Pending(root, testKey, "p1", "s"); err != nil || w.Lines != 0 || w.Deferred || w.Reason != "" {
		t.Errorf("pending once sent = %+v, %v", w, err)
	}
}

// A deferral after a batch was taken keeps what was taken and defers the
// rest.
func TestPushDeferredAfterABatch(t *testing.T) {
	root := project(t)
	old := maxBatch
	maxBatch = 1
	t.Cleanup(func() { maxBatch = old })
	log(t, root, "p1", "a", "b")
	calls := 0
	target := Target{Name: "s", Push: func(context.Context, string, []json.RawMessage) (Answer, error) {
		calls++
		if calls == 1 {
			return Answer{Accepted: 1}, nil
		}
		return Answer{Deferred: true}, nil
	}}
	res, err := Run(context.Background(), root, testKey, "p1", []Target{target}, now)
	if err != nil {
		t.Fatal(err)
	}
	if s := res.Sinks[0]; s.Sent != 1 || !s.Deferred || s.Pending != 1 || s.Partial {
		t.Errorf("result = %+v", s)
	}
	if w, _ := Pending(root, testKey, "p1", "s"); w.Lines != 1 || !w.Deferred || !w.LastPush.Equal(at) {
		t.Errorf("pending = %+v", w)
	}
	// The next push sends only the line deferred, not the batch taken.
	on := &fake{name: "s"}
	if res := run(t, root, on); res.Sinks[0].Sent != 1 || res.Sinks[0].Deferred {
		t.Fatalf("push once on = %+v", res.Sinks[0])
	}
	if got := on.sessions(t); strings.Join(got, ",") != "b" {
		t.Errorf("sink got %v, want b only", got)
	}
}

// One sink deferring does not hold back another: each has its own cursor.
func TestPushOneDefersOtherTakes(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b")
	off, on := &fake{name: "off", defer_: "not yet"}, &fake{name: "on"}
	res := run(t, root, off, on)
	if s := res.Sinks[0]; !s.Deferred || s.Sent != 0 || s.Pending != 2 || s.Err != nil {
		t.Errorf("deferring sink = %+v", s)
	}
	if s := res.Sinks[1]; s.Deferred || s.Sent != 2 || s.Pending != 0 || s.Err != nil {
		t.Errorf("taking sink = %+v", s)
	}
	if w, _ := Pending(root, testKey, "p1", "off"); w.Lines != 2 || !w.Deferred {
		t.Errorf("off pending = %+v", w)
	}
	if w, _ := Pending(root, testKey, "p1", "on"); w.Lines != 0 || w.Deferred {
		t.Errorf("on pending = %+v", w)
	}
}

// A sink that fails after deferring keeps the reason: only an answer that
// is neither an error nor a deferral clears it.
func TestPushErrorKeepsDeferral(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	run(t, root, &fake{name: "s", defer_: "off"})
	res := run(t, root, &fake{name: "s", err: errors.New("crashed")})
	if res.Sinks[0].Err == nil {
		t.Fatalf("result = %+v, want the error", res.Sinks[0])
	}
	if w, _ := Pending(root, testKey, "p1", "s"); w.Lines != 1 || !w.Deferred || w.Reason != "off" {
		t.Errorf("pending = %+v, want the deferral kept", w)
	}
}

// When the cursor's line rotated out and the sink keeps deferring, the gap
// is said once.
func TestPushGapSaidOnceWhileDeferring(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a")
	run(t, root, &fake{name: "s"})
	if err := os.Remove(filepath.Join(root, index.Dir, journal.FileName)); err != nil {
		t.Fatal(err)
	}
	log(t, root, "p1", "b")
	off := &fake{name: "s", defer_: "off"}
	if res := run(t, root, off); !res.Sinks[0].Gap || !res.Sinks[0].Deferred || res.Sinks[0].Pending != 1 {
		t.Fatalf("first deferral after the log went = %+v, want a gap", res.Sinks[0])
	}
	if res := run(t, root, off); res.Sinks[0].Gap || !res.Sinks[0].Deferred || res.Sinks[0].Pending != 1 {
		t.Errorf("second deferral = %+v, the gap said again", res.Sinks[0])
	}
	on := &fake{name: "s"}
	if res := run(t, root, on); res.Sinks[0].Gap || res.Sinks[0].Sent != 1 {
		t.Errorf("push once on = %+v", res.Sinks[0])
	}
	if got := on.sessions(t); strings.Join(got, ",") != "b" {
		t.Errorf("sink got %v, want b", got)
	}
}

// A cursor with a deferral loads, and so does one written before deferrals
// existed.
func TestCursorDeferredFieldLoads(t *testing.T) {
	root := project(t)
	if err := os.MkdirAll(filepath.Join(root, index.Dir, cursorDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"v":1,"sink":"s","project":"p1","pos":{},"pushed":0,"ts":"0001-01-01T00:00:00Z"}`,
		`{"v":1,"sink":"s","project":"p1","pos":{},"pushed":0,"ts":"0001-01-01T00:00:00Z","deferred":{"reason":"off","ts":"2026-11-07T10:00:00Z"}}`,
	} {
		if err := os.WriteFile(cursorPath(root, "s"), testKey.Seal(purpose("s"), []byte(body)), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := loadCursor(root, testKey, "s", "p1")
		if err != nil || c == nil {
			t.Fatalf("%s: loadCursor = %v, %v", body, c, err)
		}
		if want := strings.Contains(body, "deferred"); (c.Deferred != nil) != want || (want && c.Deferred.Reason != "off") {
			t.Errorf("%s: Deferred = %+v", body, c.Deferred)
		}
	}
}

// A sink that answers a partial push after deferring no longer shows the
// old reason: the reason is its last answer's.
func TestPushPartialClearsDeferral(t *testing.T) {
	root := project(t)
	log(t, root, "p1", "a", "b")
	run(t, root, &fake{name: "s", defer_: "off"})
	target := Target{Name: "s", Push: func(context.Context, string, []json.RawMessage) (Answer, error) {
		return Answer{}, nil
	}}
	res, err := Run(context.Background(), root, testKey, "p1", []Target{target}, now)
	if err != nil || !res.Sinks[0].Partial {
		t.Fatalf("partial = %+v, %v", res, err)
	}
	if w, _ := Pending(root, testKey, "p1", "s"); w.Lines != 2 || w.Deferred {
		t.Errorf("pending = %+v, want the deferral forgotten", w)
	}
}

func TestOneDeferred(t *testing.T) {
	f := &fake{name: "s", defer_: "off"}
	res := One(context.Background(), []Target{f.target()}, "p1", json.RawMessage(`{"v":1,"kind":"check"}`))
	if s := res.Sinks[0]; !s.Deferred || s.Reason != "off" || s.Partial || s.Sent != 0 || s.Err != nil {
		t.Errorf("result = %+v", s)
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

// stubLock makes Run's lock fail with err.
func stubLock(t *testing.T, err error) {
	t.Helper()
	old := lockFile
	lockFile = func(string, time.Duration, time.Duration) (func(), error) { return nil, err }
	t.Cleanup(func() { lockFile = old })
}

// A lock failure other than another push holding it is passed through, not
// reported as busy (#137).
func TestPushLockOtherErrorIsNotBusy(t *testing.T) {
	root := project(t)
	stubLock(t, &fs.PathError{Op: "open", Path: LockName, Err: fs.ErrPermission})
	_, err := Run(context.Background(), root, testKey, "p1", []Target{(&fake{name: "s"}).target()}, now)
	if errors.Is(err, ErrBusy) || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("err = %v, want fs.ErrPermission, not ErrBusy", err)
	}
}

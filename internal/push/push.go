// Package push sends the log to sinks for stele push
// (ADR-0014). It never prints, and starts nothing itself: a Target's Push is
// a sink handed in by cmd/stele.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
)

// LockName is the lock one stele push holds under .stele/.
const LockName = "push.lock"

// The limits of one push call (D82). Tests lower them.
var (
	maxBatch      = 500
	maxBatchBytes = 4 << 20
	lockWait      = 2 * time.Second
)

const (
	cursorVersion = 1
	cursorDir     = "push"
	maxCursor     = 64 << 10
	// lockStale is past the longest one sink call may take (an entry's
	// timeout, at most 30 minutes): the lock is refreshed before each call
	// and after each batch, so a live push is never taken over.
	lockStale = 35 * time.Minute
)

var (
	// ErrBusy is another stele push holding the lock.
	ErrBusy = errors.New("another stele push is running here")
	// ErrCursor is a cursor file stele cannot use: it is never taken for a
	// fresh start, which would send every line again.
	ErrCursor = errors.New("the push cursor cannot be used")
)

// cursorName is a sink's cursor file as the user sees it.
func cursorName(sink string) string {
	return filepath.ToSlash(filepath.Join(index.Dir, cursorDir, sink+".json"))
}

// Target is one sink: its name, and how to hand it records.
type Target struct {
	Name string
	Push func(ctx context.Context, project string, records []json.RawMessage) (accepted int, err error)
}

// SinkResult is what one sink got.
type SinkResult struct {
	Name         string
	Sent         int  // records it accepted
	Pending      int  // records left for the next push
	Partial      bool // it took fewer than it was sent
	Unverified   int  // lines skipped: not signed by this machine's key
	OtherProject int  // lines skipped: another project's
	Gap          bool // lines rotated out of the log before they were sent
	Err          error
}

// Result is what a push did, a sink at a time, in order.
type Result struct{ Sinks []SinkResult }

// cursor is how far a push got for one sink, sealed in
// .stele/push/<sink>.json.
type cursor struct {
	V       int         `json:"v"`
	Sink    string      `json:"sink"`
	Project string      `json:"project"`
	Pos     journal.Pos `json:"pos"`
	Pushed  int         `json:"pushed"`
	TS      time.Time   `json:"ts"`
}

func cursorPath(root, sink string) string {
	return filepath.Join(root, index.Dir, cursorDir, sink+".json")
}

func purpose(sink string) string { return "push:" + sink }

// loadCursor reads sink's cursor for project; nil when there is none yet.
// One signed for another project, or not by key, is ErrCursor.
func loadCursor(root string, key sign.Key, sink, project string) (*cursor, error) {
	data, err := safefile.ReadRegular(cursorPath(root, sink), maxCursor)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	body, err := key.Open(purpose(sink), data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not signed by this machine's key: delete it to send every line again", ErrCursor, cursorName(sink))
	}
	var c cursor
	if err := json.Unmarshal(body, &c); err != nil || c.V != cursorVersion || c.Sink != sink {
		return nil, fmt.Errorf("%w: %s holds no cursor for %s", ErrCursor, cursorName(sink), sink)
	}
	if c.Project != "" && c.Project != project {
		return nil, fmt.Errorf("%w: %s is for project %s, and stele.lock is for %s: delete it to send this project's lines", ErrCursor, cursorName(sink), c.Project, project)
	}
	return &c, nil
}

// saveCursor writes c. It refuses to move a cursor back: one on disk that
// counts more lines pushed, or is further on in the same file, was written
// by a push that went further.
func saveCursor(root string, key sign.Key, c *cursor) error {
	if old, err := loadCursor(root, key, c.Sink, c.Project); err == nil && old != nil {
		if old.Pushed > c.Pushed || (old.Pos.File == c.Pos.File && old.Pos.Offset > c.Pos.Offset) {
			return fmt.Errorf("%w: %s is further on than this push: another push ran", ErrCursor, cursorName(c.Sink))
		}
	}
	if err := safefile.Dir(filepath.Join(root, index.Dir, cursorDir)); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return safefile.Replace(cursorPath(root, c.Sink), key.Seal(purpose(c.Sink), data))
}

// line is a line waiting to be sent.
type line struct {
	rec json.RawMessage
	at  journal.Pos
}

// pending is the lines after c that carry project, and the Pos past the
// last line read, sent or skipped.
func pending(root string, key sign.Key, project string, c *cursor) (out []line, end *journal.Pos, r SinkResult, err error) {
	var after *journal.Pos
	if c != nil {
		after = &c.Pos
	}
	r.Unverified, r.Gap, err = journal.ReadAfter(root, key, after, func(_ journal.Kind, raw, body []byte, at journal.Pos) error {
		p := at
		end = &p
		var h struct {
			Project string `json:"project"`
		}
		if json.Unmarshal(body, &h) != nil || h.Project != project {
			r.OtherProject++
			return nil
		}
		out = append(out, line{rec: append(json.RawMessage{}, raw...), at: at})
		return nil
	})
	return out, end, r, err
}

// Run sends each target, in order, the log's lines under root that key
// signed and that carry project, after where it last stopped, in batches,
// and moves its cursor after each batch by what it accepted. A target that
// fails stops for this run; the others go on. It holds root's push lock.
// An error is a push that could not run at all.
func Run(ctx context.Context, root string, key sign.Key, project string, targets []Target, now func() time.Time) (Result, error) {
	var res Result
	if len(key) == 0 {
		return res, errors.New("no key on this machine: there is no log to push")
	}
	dir := filepath.Join(root, index.Dir)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		for _, t := range targets {
			res.Sinks = append(res.Sinks, SinkResult{Name: t.Name})
		}
		return res, nil // stele never ran here: no log
	}
	lockPath := filepath.Join(dir, LockName)
	unlock, err := safefile.Lock(lockPath, lockWait, lockStale)
	if err != nil {
		return res, fmt.Errorf("%w (%v)", ErrBusy, err)
	}
	defer unlock()
	held := func() { _ = safefile.Refresh(lockPath) }
	for _, t := range targets {
		res.Sinks = append(res.Sinks, pushOne(ctx, root, key, project, t, now, held))
	}
	return res, nil
}

func pushOne(ctx context.Context, root string, key sign.Key, project string, t Target, now func() time.Time, held func()) SinkResult {
	c, err := loadCursor(root, key, t.Name, project)
	if err != nil {
		return SinkResult{Name: t.Name, Err: err}
	}
	lines, end, r, err := pending(root, key, project, c)
	r.Name = t.Name
	if err != nil {
		r.Err = err
		return r
	}
	if c == nil {
		c = &cursor{V: cursorVersion, Sink: t.Name}
	}
	c.Project = project
	for len(lines) > 0 {
		n := batchLen(lines)
		recs := make([]json.RawMessage, n)
		for i := range recs {
			recs[i] = lines[i].rec
		}
		held()
		accepted, err := t.Push(ctx, project, recs)
		held()
		if accepted > 0 {
			c.Pos, c.Pushed, c.TS = lines[accepted-1].at, c.Pushed+accepted, now().UTC()
			if serr := saveCursor(root, key, c); serr != nil && err == nil {
				err = serr
			}
			r.Sent += accepted
			lines = lines[accepted:]
		}
		if err != nil {
			r.Err, r.Pending = err, len(lines)
			return r
		}
		if accepted < n {
			r.Partial, r.Pending = true, len(lines)
			return r
		}
	}
	// Everything was sent: lines skipped after the last one need not be
	// read again. After a gap with nothing left to read, the cursor goes back
	// to the start of the log, so the gap is said once.
	switch {
	case end != nil && (end.Offset != c.Pos.Offset || end.File != c.Pos.File):
		c.Pos = *end
	case end == nil && r.Gap && c.Pos.File != "":
		c.Pos = journal.Pos{}
	default:
		return r
	}
	if err := saveCursor(root, key, c); err != nil {
		r.Err = err
	}
	return r
}

// batchLen is how many of lines go in one call: at most maxBatch, and at
// most maxBatchBytes of records, but always one.
func batchLen(lines []line) int {
	n, size := 0, 0
	for n < len(lines) && n < maxBatch {
		size += len(lines[n].rec) + 1
		if n > 0 && size > maxBatchBytes {
			break
		}
		n++
	}
	return n
}

// One hands rec, one record, to each target, and remembers nothing: for a
// run that has no log to push (D84).
func One(ctx context.Context, targets []Target, project string, rec json.RawMessage) Result {
	var res Result
	for _, t := range targets {
		r := SinkResult{Name: t.Name}
		n, err := t.Push(ctx, project, []json.RawMessage{rec})
		r.Sent, r.Err = n, err
		if err == nil && n < 1 {
			r.Partial, r.Pending = true, 1
		}
		res.Sinks = append(res.Sinks, r)
	}
	return res
}

// Pending is how many of the log's lines for project wait for sink, and
// when it last pushed (zero for never). It reads, sends and writes nothing
// else: stele status.
func Pending(root string, key sign.Key, project, sink string) (n int, last time.Time, err error) {
	c, err := loadCursor(root, key, sink, project)
	if err != nil {
		return 0, time.Time{}, err
	}
	lines, _, _, err := pending(root, key, project, c)
	if c != nil {
		last = c.TS
	}
	return len(lines), last, err
}

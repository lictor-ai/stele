package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
)

// sessions reads the sessions of the lines after pos, with the pos of the
// last one read.
func sessions(t *testing.T, root string, after *Pos) (got []string, last *Pos, bad int, gap bool) {
	t.Helper()
	bad, gap, err := ReadAfter(root, testKey, after, func(_ Kind, line, body []byte, at Pos) error {
		h := struct {
			Session string `json:"session"`
		}{}
		if err := json.Unmarshal(body, &h); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(line), `"}`) || !strings.Contains(string(line), `"mac":"`) {
			t.Errorf("line is not the signed line: %s", line)
		}
		got = append(got, h.Session)
		p := at
		last = &p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, last, bad, gap
}

func appendSessions(t *testing.T, root string, ss ...string) {
	t.Helper()
	for _, s := range ss {
		if err := Append(root, testKey, &LoadedRecord{Header: head(s)}); err != nil {
			t.Fatal(err)
		}
	}
}

func rotate(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, index.Dir)
	if err := os.Rename(filepath.Join(dir, FileName), filepath.Join(dir, Rotated)); err != nil {
		t.Fatal(err)
	}
}

func TestReadAfterNilReadsEverything(t *testing.T) {
	root := project(t)
	appendSessions(t, root, "a")
	rotate(t, root)
	appendSessions(t, root, "b", "c")
	got, _, bad, gap := sessions(t, root, nil)
	if strings.Join(got, ",") != "a,b,c" || bad != 0 || gap {
		t.Errorf("got %v, %d bad, gap %v", got, bad, gap)
	}
}

func TestReadAfterResumes(t *testing.T) {
	root := project(t)
	appendSessions(t, root, "a", "b")
	_, pos, _, _ := sessions(t, root, nil)
	appendSessions(t, root, "c")
	got, last, _, gap := sessions(t, root, pos)
	if strings.Join(got, ",") != "c" || gap {
		t.Errorf("after b: got %v, gap %v; want c", got, gap)
	}
	if got, _, _, _ := sessions(t, root, last); len(got) != 0 {
		t.Errorf("after c: got %v, want nothing", got)
	}
}

func TestReadAfterAcrossRotation(t *testing.T) {
	root := project(t)
	appendSessions(t, root, "a", "b")
	_, pos, _, _ := sessions(t, root, &Pos{}) // a cursor of nothing: a gap, everything
	appendSessions(t, root, "c")
	rotate(t, root)
	appendSessions(t, root, "d")
	got, _, _, gap := sessions(t, root, pos)
	if strings.Join(got, ",") != "c,d" || gap {
		t.Errorf("got %v, gap %v; want c,d", got, gap)
	}
}

func TestReadAfterRotatedTwiceIsAGap(t *testing.T) {
	root := project(t)
	appendSessions(t, root, "a")
	_, pos, _, _ := sessions(t, root, nil)
	appendSessions(t, root, "b")
	rotate(t, root)
	appendSessions(t, root, "c")
	rotate(t, root)
	appendSessions(t, root, "d")
	got, _, _, gap := sessions(t, root, pos)
	if strings.Join(got, ",") != "c,d" || !gap {
		t.Errorf("got %v, gap %v; want c,d and a gap", got, gap)
	}
}

func TestReadAfterEditedFileScansForTheLine(t *testing.T) {
	root := project(t)
	appendSessions(t, root, "a", "b")
	_, pos, _, _ := sessions(t, root, nil)
	appendSessions(t, root, "c")
	// A line inserted before the cursor's moves its offset.
	path := filepath.Join(root, index.Dir, FileName)
	data, _ := os.ReadFile(path)
	first, rest, _ := strings.Cut(string(data), "\n")
	if err := os.WriteFile(path, []byte(first+"\n"+"not a line\n"+rest), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, _, gap := sessions(t, root, pos)
	if strings.Join(got, ",") != "c" || gap {
		t.Errorf("got %v, gap %v; want c", got, gap)
	}
}

func TestReadAfterSkipsUnverified(t *testing.T) {
	root := project(t)
	appendSessions(t, root, "a")
	path := filepath.Join(root, index.Dir, FileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"v":1,"kind":"loaded","session":"forged","mac":"` + strings.Repeat("0", 64) + `"}` + "\n")
	f.Close()
	appendSessions(t, root, "b")
	got, _, bad, _ := sessions(t, root, nil)
	if strings.Join(got, ",") != "a,b" || bad != 1 {
		t.Errorf("got %v, %d bad; want a,b and 1 bad", got, bad)
	}
}

func TestReadAfterNoLog(t *testing.T) {
	got, last, bad, gap := sessions(t, t.TempDir(), nil)
	if len(got) != 0 || last != nil || bad != 0 || gap {
		t.Errorf("got %v %v %d %v", got, last, bad, gap)
	}
}

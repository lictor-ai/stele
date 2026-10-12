package lock

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/lictor-ai/stele/internal/source"
)

// ids returns a newID that mints new-1, new-2, ...
func ids() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("new-%d", n)
	}
}

// md is a stele.md rule at position pos.
func md(pos int, body string) source.Rule {
	return source.Rule{Source: "stelemd", Position: pos, Body: body}
}

// entry is the lock entry for r with id.
func entry(id string, r source.Rule) Entry {
	return Entry{ID: id, Source: r.Source, Hash: source.Hash(r.Body), Position: r.Position}
}

// lockOf is a lock holding rules under the given ids.
func lockOf(ids []string, rules ...source.Rule) File {
	f := File{Version: 1, Project: "proj"}
	for i, r := range rules {
		f.Rules = append(f.Rules, entry(ids[i], r))
	}
	return f
}

const (
	sql   = "Never build SQL by string concatenation."
	sql2  = "Never build SQL by concatenating strings. Use parameterised queries."
	http  = "Every HTTP handler goes through the middleware chain."
	creds = "Never put credentials in source code."
)

func TestReconcile(t *testing.T) {
	ab := lockOf([]string{"a", "b"}, md(0, sql), md(1, http))
	abc := lockOf([]string{"a", "b", "c"}, md(0, sql), md(1, http), md(2, creds))

	tests := []struct {
		name  string
		rules []source.Rule
		prev  File
		want  []string
		rep   Report
	}{
		{
			name:  "first compile mints every id",
			rules: []source.Rule{md(0, sql), md(1, http)},
			prev:  File{},
			want:  []string{"new-1", "new-2"},
			rep:   Report{Minted: 2},
		},
		{
			name:  "unchanged",
			rules: []source.Rule{md(0, sql), md(1, http)},
			prev:  ab,
			want:  []string{"a", "b"},
			rep:   Report{ByHash: 2},
		},
		{
			name:  "reformatted body is the same hash",
			rules: []source.Rule{md(0, "never build  SQL\nby string concatenation"), md(1, http)},
			prev:  ab,
			want:  []string{"a", "b"},
			rep:   Report{ByHash: 2},
		},
		{
			name:  "reorder",
			rules: []source.Rule{md(0, http), md(1, sql)},
			prev:  ab,
			want:  []string{"b", "a"},
			rep:   Report{ByHash: 2},
		},
		{
			name:  "reword keeps the id by position, and says so",
			rules: []source.Rule{md(0, sql2), md(1, http)},
			prev:  ab,
			want:  []string{"a", "b"},
			rep:   Report{ByHash: 1, ByPosition: 1, Guessed: []string{"a"}},
		},
		{
			name:  "insert at the top",
			rules: []source.Rule{md(0, creds), md(1, sql), md(2, http)},
			prev:  ab,
			want:  []string{"new-1", "a", "b"},
			rep:   Report{ByHash: 2, Minted: 1},
		},
		{
			name:  "delete",
			rules: []source.Rule{md(0, sql), md(1, creds)},
			prev:  abc,
			want:  []string{"a", "c"},
			rep:   Report{ByHash: 2, Dropped: 1},
		},
		{
			// The documented mismatch: reworded sql lands at position 1, where
			// http was, so it takes http's id, and http's old id is dropped.
			name:  "reword plus reorder guesses wrong, visibly",
			rules: []source.Rule{md(0, creds), md(1, sql2)},
			prev:  abc,
			want:  []string{"c", "b"},
			rep:   Report{ByHash: 1, ByPosition: 1, Dropped: 1, Guessed: []string{"b"}},
		},
		{
			name: "a rule moving to another source keeps its id",
			rules: []source.Rule{
				md(0, http),
				{Source: "cursor", Position: 0, Body: sql},
			},
			prev: ab,
			want: []string{"b", "a"},
			rep:  Report{ByHash: 2},
		},
		{
			name: "positions are only compared within one source",
			rules: []source.Rule{
				md(0, sql),
				{Source: "cursor", Position: 1, Body: sql2},
			},
			prev: ab,
			want: []string{"a", "new-1"},
			rep:  Report{ByHash: 1, Minted: 1, Dropped: 1},
		},
		{
			name:  "two rules with one body keep their own ids",
			rules: []source.Rule{md(0, http), md(1, sql), md(2, sql)},
			prev:  lockOf([]string{"x", "y"}, md(0, sql), md(1, sql)),
			want:  []string{"new-1", "y", "x"},
			rep:   Report{ByHash: 2, Minted: 1},
		},
		{
			name: "a supplied id is kept",
			rules: []source.Rule{
				md(0, sql),
				{Source: "org", Position: 0, Body: creds, ID: "ORG-1"},
			},
			prev: lockOf([]string{"a", "ORG-1"}, md(0, sql), source.Rule{Source: "org", Position: 0, Body: "old wording"}),
			want: []string{"a", "ORG-1"},
			rep:  Report{Supplied: 1, ByHash: 1},
		},
		{
			name: "a source that starts supplying ids rekeys the rule",
			rules: []source.Rule{
				{Source: "org", Position: 0, Body: sql, ID: "ORG-1"},
				md(0, http),
			},
			prev: ab,
			want: []string{"ORG-1", "b"},
			rep:  Report{Supplied: 1, ByHash: 1, Rekeyed: []Rekey{{Old: "a", New: "ORG-1"}}},
		},
		{
			name: "rekeying takes the entry at the rule's own position",
			rules: []source.Rule{
				md(0, sql),
				{Source: "stelemd", Position: 1, Body: sql, ID: "X"},
			},
			prev: lockOf([]string{"a", "b"}, md(0, sql), md(1, sql)),
			want: []string{"a", "X"},
			rep:  Report{Supplied: 1, ByHash: 1, Rekeyed: []Rekey{{Old: "b", New: "X"}}},
		},
		{
			name: "a supplied id taken from another rule is not inherited by position",
			rules: []source.Rule{
				md(0, sql2),
				{Source: "org", Position: 0, Body: http, ID: "a"},
			},
			prev: ab,
			want: []string{"new-1", "a"},
			rep:  Report{Supplied: 1, Minted: 1, Dropped: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, next, rep, err := Reconcile(tt.rules, tt.prev, ids())
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ids (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.rep, rep, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("report (-want +got):\n%s", diff)
			}
			var entries []Entry
			for i, r := range tt.rules {
				entries = append(entries, entry(tt.want[i], r))
			}
			if diff := cmp.Diff(entries, next.Rules); diff != "" {
				t.Errorf("next.Rules (-want +got):\n%s", diff)
			}
			if next.Version != 1 {
				t.Errorf("next.Version = %d, want 1", next.Version)
			}
			if err := Validate(next); err != nil {
				t.Errorf("Validate(next): %v", err)
			}
		})
	}
}

func TestReconcileProject(t *testing.T) {
	rules := []source.Rule{md(0, sql)}
	_, first, _, err := Reconcile(rules, File{}, func() string { return "minted" })
	if err != nil {
		t.Fatal(err)
	}
	if first.Project != "minted" {
		t.Errorf("first compile: Project = %q, want a minted id", first.Project)
	}
	// Once set it never changes, even when every rule does.
	_, next, _, err := Reconcile([]source.Rule{md(0, http)}, first, func() string { return "other" })
	if err != nil {
		t.Fatal(err)
	}
	if next.Project != "minted" {
		t.Errorf("later compile: Project = %q, want %q", next.Project, "minted")
	}
}

func TestReconcileDuplicateSuppliedID(t *testing.T) {
	rules := []source.Rule{
		{Source: "org", Position: 0, Body: sql, ID: "ORG-1"},
		{Source: "org", Position: 1, Body: http, ID: "ORG-1"},
	}
	_, _, _, err := Reconcile(rules, File{}, ids())
	if !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("err = %v, want ErrDuplicateID", err)
	}
}

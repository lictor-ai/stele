package rulelist

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
)

const (
	project = "0b6f7a4e-2f4c-4b8e-9a51-3c2d1e0f9a87"
	gitName = "git:github.com/acme/guidelines/stele.md"
	commit  = "4f1c2b9e8d7a6f5e4d3c2b1a0f9e8d7c6b5a4f3e"
	moved   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

var pin = source.Pin{Source: gitName, Repo: "git@github.com:acme/guidelines.git", Ref: "main", Path: "stele.md", Commit: commit}

func TestBuild(t *testing.T) {
	idx := index.Index{
		Project: project,
		Sources: map[string]string{"stelemd": "fp", gitName: commit, "lint": "v1"},
		Rules: []index.Entry{
			{ID: "r1", Source: "stelemd", Position: 0, Body: "Never build SQL by hand."},
			{ID: "r2", Source: "stelemd", Position: 1, Scope: []string{"**/*.go"}, Body: "No panic.\n\nIn library code."},
			{ID: "r3", Source: gitName, Position: 0, Scope: []string{}, Body: "Wrap every error."},
			{ID: "r4", Source: "lint", Position: 2, Body: "Doc every export."},
		},
	}
	lf := lock.File{Project: project, Sources: []source.Pin{pin}}
	got := Build(idx, lf)
	want := List{
		V:       Version,
		Project: project,
		Rules: []Rule{
			{ID: "r1", Text: "Never build SQL by hand.", Source: Source{Kind: KindSteleMD, Name: "stelemd", File: "stele.md", Rule: 1}},
			{ID: "r2", Text: "No panic.\n\nIn library code.", Scope: []string{"**/*.go"}, Source: Source{Kind: KindSteleMD, Name: "stelemd", File: "stele.md", Rule: 2}},
			{ID: "r3", Text: "Wrap every error.", Source: Source{Kind: KindGit, Name: gitName, Repo: pin.Repo, Ref: "main", File: "stele.md", Commit: commit, Rule: 1}},
			{ID: "r4", Text: "Doc every export.", Source: Source{Kind: KindPlugin, Name: "lint", Rule: 3}},
		},
		Missing: []Source{},
		Notes:   []string{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build =\n%+v\nwant\n%+v", got, want)
	}
}

// The JSON a tool reads: "v" first, scope null for every file, empty lists
// as [], never null, and a source's unset fields left out.
func TestBuildJSON(t *testing.T) {
	idx := index.Index{
		Project: project,
		Sources: map[string]string{"stelemd": "fp"},
		Rules:   []index.Entry{{ID: "r1", Source: "stelemd", Body: "Be kind."}},
	}
	data, err := json.Marshal(Build(idx, lock.File{}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"project":"` + project + `","rules":[{"id":"r1","text":"Be kind.","scope":null,"source":{"kind":"stele.md","name":"stelemd","file":"stele.md","rule":1}}],"missing":[],"notes":[]}`
	if string(data) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", data, want)
	}

	data, _ = json.Marshal(Build(index.Index{}, lock.File{}))
	if want := `{"v":1,"project":"","rules":[],"missing":[],"notes":[]}`; string(data) != want {
		t.Errorf("empty JSON = %s, want %s", data, want)
	}
}

// A git source on the remote's default branch has no ref.
func TestBuildDefaultBranch(t *testing.T) {
	p := pin
	p.Ref = ""
	idx := index.Index{
		Sources: map[string]string{gitName: commit},
		Rules:   []index.Entry{{ID: "r1", Source: gitName, Body: "x"}},
	}
	got := Build(idx, lock.File{Sources: []source.Pin{p}}).Rules[0].Source
	if got.Ref != "" || got.Repo != p.Repo || got.Commit != commit {
		t.Errorf("source = %+v", got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), `"ref"`) {
		t.Errorf("JSON has a ref: %s", data)
	}
}

// Missing are the sources stele.lock pins or holds rules for that the
// index lacks, as on a fresh clone, each listed once, without "rule".
func TestBuildMissing(t *testing.T) {
	tests := []struct {
		name string
		idx  index.Index
		lf   lock.File
		want []Source
	}{
		{
			name: "all in the index",
			idx: index.Index{
				Sources: map[string]string{"stelemd": "fp", gitName: commit},
				Rules:   []index.Entry{{ID: "r1", Source: "stelemd"}, {ID: "g1", Source: gitName}},
			},
			lf:   lock.File{Sources: []source.Pin{pin}, Rules: []lock.Entry{{ID: "r1", Source: "stelemd"}, {ID: "g1", Source: gitName}}},
			want: []Source{},
		},
		{
			name: "fresh clone: the builtins only",
			idx: index.Index{
				Sources: map[string]string{"stelemd": "fp"},
				Rules:   []index.Entry{{ID: "r1", Source: "stelemd"}},
			},
			lf: lock.File{Sources: []source.Pin{pin}, Rules: []lock.Entry{
				{ID: "r1", Source: "stelemd"}, {ID: "g1", Source: gitName}, {ID: "g2", Source: gitName}, {ID: "p1", Source: "lint"},
			}},
			want: []Source{
				{Kind: KindGit, Name: gitName, Repo: pin.Repo, Ref: "main", File: "stele.md", Commit: commit},
				{Kind: KindPlugin, Name: "lint"},
			},
		},
		{
			name: "a pull moved the pin",
			idx: index.Index{
				Sources: map[string]string{gitName: moved},
				Rules:   []index.Entry{{ID: "g1", Source: gitName}},
			},
			lf:   lock.File{Sources: []source.Pin{pin}, Rules: []lock.Entry{{ID: "g1", Source: gitName}}},
			want: []Source{{Kind: KindGit, Name: gitName, Repo: pin.Repo, Ref: "main", File: "stele.md", Commit: commit}},
		},
		{
			name: "a pull added a rule to a source the index holds",
			idx: index.Index{
				Sources: map[string]string{"lint": "v1"},
				Rules:   []index.Entry{{ID: "p1", Source: "lint"}},
			},
			lf:   lock.File{Rules: []lock.Entry{{ID: "p1", Source: "lint"}, {ID: "p2", Source: "lint"}}},
			want: []Source{{Kind: KindPlugin, Name: "lint"}},
		},
		{
			name: "stele.md rules the index does not hold yet",
			idx:  index.Index{},
			lf:   lock.File{Rules: []lock.Entry{{ID: "r1", Source: "stelemd"}}},
			want: []Source{{Kind: KindSteleMD, Name: "stelemd", File: "stele.md"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build(tt.idx, tt.lf).Missing; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missing =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

// A git rule whose pin stele.lock lost still names its source and commit.
func TestBuildGitWithoutPin(t *testing.T) {
	idx := index.Index{
		Sources: map[string]string{gitName: commit},
		Rules:   []index.Entry{{ID: "g1", Source: gitName, Body: "x"}},
	}
	got := Build(idx, lock.File{}).Rules[0].Source
	want := Source{Kind: KindGit, Name: gitName, Commit: commit, Rule: 1}
	if got != want {
		t.Errorf("source = %+v, want %+v", got, want)
	}
}

// The rules keep the index's order, which is the rules file's.
func TestBuildKeepsIndexOrder(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{{ID: "b", Source: "lint", Position: 1}, {ID: "a", Source: "stelemd"}, {ID: "c", Source: "lint"}}}
	var ids []string
	for _, r := range Build(idx, lock.File{}).Rules {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "b,a,c" {
		t.Errorf("order = %s, want b,a,c", got)
	}
}

// Build copies what it returns: changing the list leaves the index alone.
func TestBuildCopiesScope(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{{ID: "a", Source: "stelemd", Scope: []string{"*.go"}}}}
	l := Build(idx, lock.File{})
	l.Rules[0].Scope[0] = "changed"
	if idx.Rules[0].Scope[0] != "*.go" {
		t.Error("Build shares the index's scope")
	}
}

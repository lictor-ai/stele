package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/publish"
)

// stubPublish replaces stele publish's work with one that returns res and
// err, and records what would be opened.
func stubPublish(t *testing.T, res publish.Result, err error) *[]string {
	t.Helper()
	oldRun, oldOpen := publishRun, openLink
	t.Cleanup(func() { publishRun, openLink = oldRun, oldOpen })
	publishRun = func(context.Context, string) (publish.Result, error) { return res, err }
	var opened []string
	openLink = func(_, link string) error {
		opened = append(opened, link)
		return fmt.Errorf("no browser")
	}
	return &opened
}

var sample = publish.Result{
	Repo: "git@github.com:me/rules.git", Path: "stele.md", Base: "main",
	Branch: "stele/publish/stele.md", Pushed: true,
}

func TestPublishOutput(t *testing.T) {
	with := func(f func(*publish.Result)) publish.Result {
		r := sample
		f(&r)
		return r
	}
	tests := []struct {
		name   string
		res    publish.Result
		stdout []string
		opened string
	}{
		{
			name:   "unchanged",
			res:    with(func(r *publish.Result) { r.Unchanged, r.Pushed = true, false }),
			stdout: []string{"stele.md on main in git@github.com:me/rules.git is already this stele.md: nothing to publish\n"},
		},
		{
			name: "opened",
			res:  with(func(r *publish.Result) { r.MR, r.MRAction = "https://github.com/me/rules/pull/1", "opened" }),
			stdout: []string{
				"pushed branch stele/publish/stele.md to git@github.com:me/rules.git\n",
				"merge request opened: https://github.com/me/rules/pull/1\n",
			},
			opened: "https://github.com/me/rules/pull/1",
		},
		{
			name: "reopened, not pushed",
			res: with(func(r *publish.Result) {
				r.Pushed, r.MR, r.MRAction = false, "https://github.com/me/rules/pull/1", "reopened"
			}),
			stdout: []string{
				"branch stele/publish/stele.md in git@github.com:me/rules.git already has this stele.md\n",
				"merge request reopened: https://github.com/me/rules/pull/1\n",
			},
			opened: "https://github.com/me/rules/pull/1",
		},
		{
			name: "no login",
			res: with(func(r *publish.Result) {
				r.Why = "no login found for github.com"
				r.Page = "https://github.com/me/rules/compare/main...stele/publish/stele.md?expand=1"
			}),
			stdout: []string{
				"no merge request opened: no login found for github.com\n",
				"open it here: https://github.com/me/rules/compare/main...stele/publish/stele.md?expand=1\n",
			},
			opened: "https://github.com/me/rules/compare/main...stele/publish/stele.md?expand=1",
		},
		{
			name: "another host",
			res:  with(func(r *publish.Result) { r.Repo = "https://git.example.com/me/rules.git" }),
			stdout: []string{
				"open a merge request in https://git.example.com/me/rules.git from branch stele/publish/stele.md into main\n",
			},
		},
		{
			name: "escape sequences removed",
			res: with(func(r *publish.Result) {
				r.Why = "\x1b[31mbad\x1b[0m"
				r.Page = "https://github.com/me/rules/compare/x"
			}),
			stdout: []string{"no merge request opened: bad\n"},
			opened: "https://github.com/me/rules/compare/x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project(t, "x\n")
			opened := stubPublish(t, tt.res, nil)
			code, stdout, stderr := runCmd("publish")
			if code != exitOK || stderr != "" {
				t.Errorf("code %d, stderr %q", code, stderr)
			}
			for _, want := range tt.stdout {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout =\n%s\nwant it to contain\n%s", stdout, want)
				}
			}
			if strings.Contains(stdout, "\x1b") {
				t.Errorf("stdout holds an escape: %q", stdout)
			}
			switch {
			case tt.opened == "" && len(*opened) != 0:
				t.Errorf("opened %q, want nothing", *opened)
			case tt.opened != "" && (len(*opened) != 1 || (*opened)[0] != tt.opened):
				t.Errorf("opened %q, want %q", *opened, tt.opened)
			}
		})
	}
}

func TestPublishErrorExits1(t *testing.T) {
	project(t, "x\n")
	stubPublish(t, publish.Result{}, fmt.Errorf("%w: \x1b[2Jboom", publish.ErrGit))
	code, stdout, stderr := runCmd("publish")
	if code != exitFailed || stdout != "" {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	if !strings.HasPrefix(stderr, "stele publish: git failed: boom") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestPublishExtraArgument(t *testing.T) {
	project(t, "x\n")
	if code, _, stderr := runCmd("publish", "extra"); code != exitCantRun || !strings.Contains(stderr, "unexpected argument") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

// The real stele publish, up to the first refusal: no network is reached.
func TestPublishRefusals(t *testing.T) {
	t.Run("no publish key", func(t *testing.T) {
		project(t, "x\n")
		code, _, stderr := runCmd("publish")
		if code != exitFailed || !strings.Contains(stderr, `{"publish": {"repo": `) {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})
	t.Run("bad repo", func(t *testing.T) {
		root := project(t, "x\n")
		writeTestFile(t, filepath.Join(root, "stele.json"), `{"publish": {"repo": "file:///x"}}`)
		code, _, stderr := runCmd("publish")
		if code != exitFailed || !strings.Contains(stderr, "repo") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})
	t.Run("edited stele.md", func(t *testing.T) {
		root := gitProject(t, "x\n")
		writeTestFile(t, filepath.Join(root, "stele.json"), `{"publish": {"repo": "git@github.com:me/rules.git"}}`)
		writeTestFile(t, filepath.Join(root, "stele.md"), "y\n")
		code, _, stderr := runCmd("publish")
		if code != exitFailed || !strings.Contains(stderr, "commit it first") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

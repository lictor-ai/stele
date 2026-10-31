package publish

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadConfigMissing(t *testing.T) {
	for name, root := range map[string]string{
		"no file":        t.TempDir(),
		"no publish key": writeJSON(t, `{"block_on_error": true}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(root)
			if !errors.Is(err, ErrNoConfig) {
				t.Fatalf("err = %v, want ErrNoConfig", err)
			}
			for _, want := range []string{`"publish"`, `"repo"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to show %s", err, want)
				}
			}
		})
	}
}

func TestLoadConfigDefaultPath(t *testing.T) {
	root := writeJSON(t, `{"publish": {"repo": "git@github.com:me/g.git"}}`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "git@github.com:me/g.git" || cfg.Path != "stele.md" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadConfigByteOrderMark(t *testing.T) {
	root := writeJSON(t, "\xef\xbb\xbf"+`{"publish": {"repo": "git@github.com:me/g.git"}}`)
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "git@github.com:me/g.git" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	for name, content := range map[string]string{
		"not json":           `{`,
		"publish not object": `{"publish": "x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeJSON(t, content)); err == nil || errors.Is(err, ErrNoConfig) {
				t.Errorf("err = %v, want a JSON error", err)
			}
		})
	}
}

func TestValidateRepo(t *testing.T) {
	good := []string{
		"https://github.com/me/g.git",
		"https://user@github.com/me/g",
		"ssh://git@gitlab.com/g/s/p.git",
		"ssh://gitlab.com:2222/g/p",
		"git@github.com:me/g.git",
		"https://git.example.com:8443/x",
	}
	for _, repo := range good {
		if err := validateRepo(repo); err != nil {
			t.Errorf("validateRepo(%q) = %v, want nil", repo, err)
		}
	}
	bad := []string{
		"",
		"-u",
		"--upload-pack=calc.exe",
		"/abs/path",
		`C:\x`,
		"C:/x",
		"./x",
		"x",
		"file:///x",
		"ext::sh -c x",
		"http://github.com/x",
		"git://github.com/x",
		"host:path",
		"git@host:",
		"git@host::x",
		"git@host:-x",
		"https://github.com",
		"https://github.com/",
		"https://github.com/x?y=1",
		"https://github.com/x#y",
		"https://github.com/x y",
		"https://github.com/x\ny",
		"https://github.com/x\x1b",
		"https:///x",
		"ssh://-oProxyCommand=x/y",
		"https://user:secret@github.com/me/g.git",
		"ssh://git:secret@gitlab.com/g/p.git",
		"git:secret@github.com:me/g.git",
	}
	for _, repo := range bad {
		err := validateRepo(repo)
		if !errors.Is(err, ErrBadConfig) {
			t.Errorf("validateRepo(%q) = %v, want ErrBadConfig", repo, err)
			continue
		}
		if !strings.Contains(err.Error(), "repo") {
			t.Errorf("validateRepo(%q) = %q, want it to name repo", repo, err)
		}
	}
}

func TestValidateRepoHidesPassword(t *testing.T) {
	for _, repo := range []string{
		"https://user:secret@github.com/me/g.git",
		"ssh://git:secret@gitlab.com/g/p.git",
		"git:secret@github.com:me/g.git",
	} {
		err := validateRepo(repo)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("validateRepo(%q) = %v, want an error that does not show the password", repo, err)
		}
	}
}

func TestValidatePath(t *testing.T) {
	for _, p := range []string{"stele.md", "project-a/stele.md", "a_b/c-d/e.f.md", "_x/stele.md"} {
		if err := validatePath(p); err != nil {
			t.Errorf("validatePath(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		"", "/stele.md", "../x.md", "a/../b.md", "x..md", "a/./b.md",
		".git/x.md", ".GIT/x.md", ".hidden.md", "-a.md", "a./b.md", "a.lock/b.md",
		`a\b.md`, "a:b.md", "a//b.md", "a/", "stele.txt", "stele.MD", "a b.md",
		"a~b.md", "a^b.md", "a?b.md", "a*b.md", "a[b.md", "a@{b.md",
		strings.Repeat("a", 200) + ".md",
	}
	for _, p := range bad {
		err := validatePath(p)
		if !errors.Is(err, ErrBadConfig) {
			t.Errorf("validatePath(%q) = %v, want ErrBadConfig", p, err)
			continue
		}
		if !strings.Contains(err.Error(), "path") {
			t.Errorf("validatePath(%q) = %q, want it to name path", p, err)
		}
	}
}

func TestLoadConfigRefusesBadValues(t *testing.T) {
	for name, content := range map[string]string{
		"bad repo": `{"publish": {"repo": "file:///x"}}`,
		"no repo":  `{"publish": {"path": "a.md"}}`,
		"bad path": `{"publish": {"repo": "git@github.com:me/g.git", "path": "../x.md"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeJSON(t, content)); !errors.Is(err, ErrBadConfig) {
				t.Errorf("err = %v, want ErrBadConfig", err)
			}
		})
	}
}

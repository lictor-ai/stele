package publish

import (
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestOpenCommand(t *testing.T) {
	tests := []struct {
		goos, name string
		prefix     []string
	}{
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler"}},
		{"darwin", "open", nil},
		{"linux", "xdg-open", nil},
		{"freebsd", "xdg-open", nil},
	}
	for _, tt := range tests {
		name, prefix := openCommand(tt.goos)
		if name != tt.name || !slices.Equal(prefix, tt.prefix) {
			t.Errorf("openCommand(%s) = %s %q", tt.goos, name, prefix)
		}
	}
}

func TestOpenPassesTheLinkAsOneArgument(t *testing.T) {
	name, prefix := openCommand(runtime.GOOS)
	_, log := fakeTools(t, name)
	link := "https://github.com/me/g/compare/a...b?expand=1&x=%20;y"
	if err := Open(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	var calls []fakeCall
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if calls = fakeCalls(t, log); len(calls) > 0 {
			break
		}
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if want := append(append([]string{}, prefix...), link); !slices.Equal(calls[0].Args, want) {
		t.Errorf("args = %q, want %q", calls[0].Args, want)
	}
}

func TestOpenRefusesOpenerInProject(t *testing.T) {
	name, _ := openCommand(runtime.GOOS)
	bin, log := fakeTools(t, name)
	if err := Open(filepath.Dir(bin), "https://github.com/x"); !errors.Is(err, ErrToolRefused) {
		t.Errorf("err = %v, want ErrToolRefused", err)
	}
	time.Sleep(200 * time.Millisecond)
	if calls := fakeCalls(t, log); len(calls) != 0 {
		t.Errorf("the opener ran: %+v", calls)
	}
}

func TestOpenMissingOpener(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := Open(t.TempDir(), "https://github.com/x"); err == nil {
		t.Error("no opener: err = nil")
	}
}

func TestOpenRefusesNonHTTPS(t *testing.T) {
	name, _ := openCommand(runtime.GOOS)
	_, log := fakeTools(t, name)
	for _, link := range []string{"file:///etc/passwd", "javascript:alert(1)", "http://github.com/x"} {
		if err := Open(t.TempDir(), link); err == nil {
			t.Errorf("Open(%q) = nil", link)
		}
	}
	if calls := fakeCalls(t, log); len(calls) != 0 {
		t.Errorf("the opener ran: %+v", calls)
	}
}

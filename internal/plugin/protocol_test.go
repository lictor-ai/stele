package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidName(t *testing.T) {
	good := []string{"semgrep", "docs-site", "a", "x1", "a" + strings.Repeat("b", 63)}
	bad := []string{"", "X", "-x", "1x", "a.b", "../x", "x/y", `x\y`, "a b", "a_b", "é", "a" + strings.Repeat("b", 64)}
	for _, n := range good {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidName(n); err == nil {
			t.Errorf("ValidName(%q) = nil, want an error", n)
		}
	}
	for _, n := range []string{"stelemd", "git", "regex"} {
		if err := ValidName(n); err == nil || !strings.Contains(err.Error(), "built in") {
			t.Errorf("ValidName(%q) = %v, want built in", n, err)
		}
	}
}

func TestExe(t *testing.T) {
	if got := Exe(KindChecker, "semgrep"); got != "stele-checker-semgrep" {
		t.Errorf("Exe = %q", got)
	}
}

func TestRequestCarriesProtocolAndOp(t *testing.T) {
	got, err := Request("read", map[string]any{"root": "/r"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"op":"read","protocol":1,"root":"/r"}` {
		t.Errorf("Request = %s", got)
	}
}

func TestDecodeAnswer(t *testing.T) {
	type ans struct {
		Fingerprint string `json:"fingerprint"`
	}
	cases := []struct {
		name, in string
		want     string // "" = ok, else a word the error holds
		is       error
	}{
		{"good", `{"protocol":1,"fingerprint":"x"}`, "", nil},
		{"spaces after", "{\"protocol\":1,\"fingerprint\":\"x\"}\n\n", "", nil},
		{"unknown field ignored", `{"protocol":1,"fingerprint":"x","later":true}`, "", nil},
		{"no protocol", `{"fingerprint":"x"}`, "no protocol", ErrProtocol},
		{"protocol 2", `{"protocol":2,"error":"unsupported protocol 1; speaks [2]"}`, "speaks protocol 2", ErrProtocol},
		{"error field", `{"protocol":1,"error":"bad \u001b[31mconfig\nhere"}`, "bad config here", ErrAnswer},
		{"not json", `nope`, "not valid JSON", ErrProtocol},
		{"not an object", `[1]`, "not valid JSON", ErrProtocol},
		{"trailing", `{"protocol":1,"fingerprint":"x"} {}`, "after the answer", ErrProtocol},
		{"empty", ``, "no answer", ErrProtocol},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a ans
			err := DecodeAnswer([]byte(c.in), &a)
			if c.want == "" {
				if err != nil || a.Fingerprint != "x" {
					t.Fatalf("DecodeAnswer = %v, %+v", err, a)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) || !errors.Is(err, c.is) {
				t.Fatalf("DecodeAnswer = %v, want %q and %v", err, c.want, c.is)
			}
		})
	}
}

// fakeRunner answers from a table, by op, and records the requests.
type fakeRunner struct {
	bin     string
	findErr error
	answers map[string]string
	runErr  error
	got     []string
}

func (f *fakeRunner) Find(kind Kind, name string) (string, error) {
	if f.findErr != nil {
		return "", f.findErr
	}
	if f.bin != "" {
		return f.bin, nil
	}
	return "/bin/" + Exe(kind, name), nil
}

func (f *fakeRunner) Run(ctx context.Context, bin string, req []byte, max int, timeout time.Duration) ([]byte, error) {
	f.got = append(f.got, string(req))
	if f.runErr != nil {
		return nil, f.runErr
	}
	for op, a := range f.answers {
		if strings.Contains(string(req), `"op":"`+op+`"`) {
			return []byte(a), nil
		}
	}
	return []byte(`{"protocol":1,"error":"no fake answer"}`), nil
}

func TestDescribe(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": `{"protocol":1,"kind":"checker","protocols":[1],"version":"1.4.0"}`}}
	info, err := Describe(context.Background(), run, KindChecker, "semgrep")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindChecker || info.Version != "1.4.0" || info.Exe != "/bin/stele-checker-semgrep" {
		t.Errorf("Describe = %+v", info)
	}
}

func TestDescribeWrongKind(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": `{"protocol":1,"kind":"source","protocols":[1],"version":"1"}`}}
	_, err := Describe(context.Background(), run, KindChecker, "semgrep")
	if err == nil || !strings.Contains(err.Error(), "says it is a source, not a checker") || !errors.Is(err, ErrProtocol) {
		t.Fatalf("Describe = %v", err)
	}
}

func TestDescribeUnsupportedProtocol(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": `{"protocol":1,"kind":"source","protocols":[2],"version":"2.0.0"}`}}
	_, err := Describe(context.Background(), run, KindSource, "docs-site")
	want := "stele-source-docs-site 2.0.0 speaks protocol [2]; this stele speaks 1"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Describe = %v, want %q", err, want)
	}
}

func TestDescribeBadVersion(t *testing.T) {
	for _, v := range []string{`""`, `"` + strings.Repeat("1", 65) + `"`, `"1\u001b"`} {
		run := &fakeRunner{answers: map[string]string{"describe": `{"protocol":1,"kind":"source","protocols":[1],"version":` + v + `}`}}
		if _, err := Describe(context.Background(), run, KindSource, "x"); !errors.Is(err, ErrProtocol) {
			t.Errorf("version %s: Describe = %v, want ErrProtocol", v, err)
		}
	}
}

func TestErrorNamesPlugin(t *testing.T) {
	cases := []struct {
		err  *Error
		want string
	}{
		{&Error{Kind: KindChecker, Name: "semgrep", List: "checkers", Err: ErrPluginNotFound},
			`checker "semgrep": stele-checker-semgrep not found on PATH: install it, or remove "semgrep" from "checkers" in stele.json`},
		{&Error{Kind: KindChecker, Name: "semgrep", Err: errors.New("stele-checker-semgrep at /r/bin/x: refused: it is inside the project")},
			`checker "semgrep": stele-checker-semgrep at /r/bin/x: refused: it is inside the project`},
		{&Error{Kind: KindChecker, Name: "semgrep", Exe: "stele-checker-semgrep", Op: "check", Err: &ExitError{Code: 3, Stderr: "boom\nbang"}},
			`stele-checker-semgrep check: exited with status 3: boom bang`},
		{&Error{Kind: KindChecker, Name: "semgrep", Exe: "stele-checker-semgrep", Op: "check", Err: timeoutAfter(5 * time.Minute)},
			`stele-checker-semgrep check: no answer in 5m0s, stopped: set a longer "timeout" for "semgrep" in stele.json`},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() =\n %s\nwant\n %s", got, c.want)
		}
	}
	e := &Error{Err: ErrPluginNotFound}
	if !errors.Is(e, ErrPluginNotFound) {
		t.Error("Error does not unwrap to its sentinel")
	}
}

func timeoutAfter(d time.Duration) error { return Timeout(d) }

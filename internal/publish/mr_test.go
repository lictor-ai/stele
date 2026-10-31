package publish

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// setTokens makes every token host offer these logins, in order.
func setTokens(t *testing.T, toks ...string) *atomic.Int32 {
	t.Helper()
	var asked atomic.Int32
	old := tokenSources
	tokenSources = func(_ gitCmd, _, _ string, r remote) []tokenSource {
		if !r.tokenHost() {
			return nil
		}
		var out []tokenSource
		for i, tok := range toks {
			out = append(out, tokenSource{name: "src" + string(rune('1'+i)), get: func(context.Context) string {
				asked.Add(1)
				return tok
			}})
		}
		return out
	}
	t.Cleanup(func() { tokenSources = old })
	return &asked
}

func TestRunOpensPullRequest(t *testing.T) {
	f := newFixture(t, "git@github.com:me/rules.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	setTokens(t, "bad", "good")
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.Header.Get("Authorization") != "Bearer good":
			writeJSONTo(w, 401, map[string]any{})
		case r.Method == "GET":
			writeJSONTo(w, 200, []any{})
		default:
			writeJSONTo(w, 201, map[string]any{"number": 1, "html_url": "https://github.com/me/rules/pull/1"})
		}
	})
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.MR != "https://github.com/me/rules/pull/1" || res.MRAction != "opened" || res.Page != "" || res.Why != "" {
		t.Errorf("result = %+v", res)
	}
	auths := map[string]bool{}
	for _, r := range s.requests() {
		auths[r.Auth] = true
	}
	if !auths["Bearer bad"] || !auths["Bearer good"] {
		t.Errorf("auths = %v, want both logins tried", auths)
	}
}

func TestRunAllRejected(t *testing.T) {
	f := newFixture(t, "git@github.com:me/rules.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	setTokens(t, "a", "b")
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, 403, map[string]any{}) })
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Why, "rejected") || res.Page != "https://github.com/me/rules/compare/main...stele/publish/stele.md?expand=1" {
		t.Errorf("result = %+v", res)
	}
}

func TestRunNoLogin(t *testing.T) {
	f := newFixture(t, "git@gitlab.com:g/s/rules.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	setTokens(t)
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Why, "no login found for gitlab.com") || !strings.HasPrefix(res.Page, "https://gitlab.com/g/s/rules/-/merge_requests/new?") {
		t.Errorf("result = %+v", res)
	}
}

func TestRunAPIError(t *testing.T) {
	f := newFixture(t, "git@github.com:me/rules.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	setTokens(t, "t")
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, 502, map[string]any{}) })
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Why, "502") || res.Page == "" {
		t.Errorf("result = %+v", res)
	}
}

func TestRunForeignLinkIgnored(t *testing.T) {
	f := newFixture(t, "git@github.com:me/rules.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	setTokens(t, "t")
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		writeJSONTo(w, 200, []map[string]any{{"number": 1, "html_url": "https://evil.example/x"}})
	})
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.MR != "" || res.Page == "" {
		t.Errorf("result = %+v, want the link dropped for the prefilled page", res)
	}
}

func TestRunReopensWhenUnchanged(t *testing.T) {
	f := newFixture(t, "git@gitlab.com:g/s/rules.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	setTokens(t, "t")
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.RawQuery, "state=opened"):
			writeJSONTo(w, 200, []any{})
		case r.Method == "GET":
			writeJSONTo(w, 200, []map[string]any{{"iid": 2, "source_project_id": 1, "target_project_id": 1, "web_url": "https://gitlab.com/g/s/rules/-/merge_requests/2"}})
		case r.Method == "PUT":
			writeJSONTo(w, 200, map[string]any{"iid": 2, "web_url": "https://gitlab.com/g/s/rules/-/merge_requests/2"})
		default:
			writeJSONTo(w, 201, map[string]any{"iid": 1, "web_url": "https://gitlab.com/g/s/rules/-/merge_requests/1"})
		}
	})
	if res, err := Run(context.Background(), f.root); err != nil || res.MRAction != "opened" {
		t.Fatalf("first run: %+v, %v", res, err)
	}
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed || res.MRAction != "reopened" {
		t.Errorf("second run = %+v, want reopened without a push", res)
	}
}

func TestRunOtherHostAsksNoLogin(t *testing.T) {
	f := newFixture(t, "git@gitlab.attacker.example:x/y.git", "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	asked := setTokens(t, "t")
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 0 || res.Page != "" || res.Why != "" {
		t.Errorf("result = %+v, logins asked %d", res, asked.Load())
	}
}

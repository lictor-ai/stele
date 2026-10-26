package publish

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// apiServer is a fake GitHub or GitLab API over TLS. handle answers a
// request; every request is recorded.
type apiServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	Method, URI, Auth, Version, Body string
}

func (s *apiServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded{}, s.reqs...)
}

// newAPI starts a fake API and points githubAPI and gitlabAPI at it.
func newAPI(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body string)) *apiServer {
	t.Helper()
	s := &apiServer{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{r.Method, r.RequestURI, r.Header.Get("Authorization"), r.Header.Get("X-GitHub-Api-Version"), string(body)})
		s.mu.Unlock()
		handle(w, r, string(body))
	}))
	t.Cleanup(s.Close)
	oldGH, oldGL, oldClient := githubAPI, gitlabAPI, httpClient
	githubAPI, gitlabAPI = s.URL, s.URL+"/api/v4"
	client := s.Client()
	client.CheckRedirect = noRedirect
	httpClient = client
	t.Cleanup(func() { githubAPI, gitlabAPI, httpClient = oldGH, oldGL, oldClient })
	return s
}

func writeJSONTo(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var ghRemote = parseRemote("git@github.com:me/rules.git")

var ghReq = mrRequest{Base: "main", Branch: "stele/publish/stele.md", Title: "stele: publish stele.md", Body: "b", Pushed: true}

func TestGithubFindsOpen(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		writeJSONTo(w, 200, []map[string]any{{"number": 3, "html_url": "https://github.com/me/rules/pull/3"}})
	})
	got, err := githubMR(context.Background(), ghRemote, "t", ghReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "found" || got.Link != "https://github.com/me/rules/pull/3" {
		t.Errorf("got %+v", got)
	}
	reqs := s.requests()
	if len(reqs) != 1 || reqs[0].Method != "GET" {
		t.Fatalf("requests = %+v", reqs)
	}
	for _, want := range []string{"/repos/me/rules/pulls?", "state=open", "head=me%3Astele%2Fpublish%2Fstele.md", "base=main"} {
		if !strings.Contains(reqs[0].URI, want) {
			t.Errorf("uri %s lacks %s", reqs[0].URI, want)
		}
	}
	if reqs[0].Auth != "Bearer t" || reqs[0].Version != "2022-11-28" {
		t.Errorf("headers: auth %q version %q", reqs[0].Auth, reqs[0].Version)
	}
}

func TestGithubOpens(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Method == "GET" {
			writeJSONTo(w, 200, []any{})
			return
		}
		writeJSONTo(w, 201, map[string]any{"number": 4, "html_url": "https://github.com/me/rules/pull/4"})
	})
	got, err := githubMR(context.Background(), ghRemote, "t", ghReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "opened" || got.Link != "https://github.com/me/rules/pull/4" {
		t.Errorf("got %+v", got)
	}
	reqs := s.requests()
	if len(reqs) != 2 || reqs[1].Method != "POST" {
		t.Fatalf("requests = %+v", reqs)
	}
	var body map[string]string
	json.Unmarshal([]byte(reqs[1].Body), &body)
	if body["head"] != "stele/publish/stele.md" || body["base"] != "main" || body["title"] != "stele: publish stele.md" {
		t.Errorf("body = %v", body)
	}
}

func TestGithubReopens(t *testing.T) {
	merged := "2026-01-01T00:00:00Z"
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.RawQuery, "state=open"):
			writeJSONTo(w, 200, []any{})
		case r.Method == "GET":
			writeJSONTo(w, 200, []map[string]any{
				{"number": 9, "html_url": "https://github.com/me/rules/pull/9", "merged_at": merged},
				{"number": 7, "html_url": "https://github.com/me/rules/pull/7"},
			})
		case r.Method == "PATCH":
			writeJSONTo(w, 200, map[string]any{"number": 7, "html_url": "https://github.com/me/rules/pull/7"})
		}
	})
	req := ghReq
	req.Pushed = false
	got, err := githubMR(context.Background(), ghRemote, "t", req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "reopened" || got.Link != "https://github.com/me/rules/pull/7" {
		t.Errorf("got %+v", got)
	}
	reqs := s.requests()
	last := reqs[len(reqs)-1]
	if last.Method != "PATCH" || !strings.HasSuffix(last.URI, "/pulls/7") || !strings.Contains(last.Body, `"state":"open"`) {
		t.Errorf("last request = %+v", last)
	}
}

func TestGithubReopenRefusedOpensNew(t *testing.T) {
	newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.RawQuery, "state=open"):
			writeJSONTo(w, 200, []any{})
		case r.Method == "GET":
			writeJSONTo(w, 200, []map[string]any{{"number": 7, "html_url": "https://github.com/me/rules/pull/7"}})
		case r.Method == "PATCH":
			writeJSONTo(w, 422, map[string]any{"message": "cannot reopen"})
		case r.Method == "POST":
			writeJSONTo(w, 201, map[string]any{"number": 8, "html_url": "https://github.com/me/rules/pull/8"})
		}
	})
	req := ghReq
	req.Pushed = false
	got, err := githubMR(context.Background(), ghRemote, "t", req)
	if err != nil || got.Action != "opened" {
		t.Errorf("got %+v, %v; want opened", got, err)
	}
}

func TestGithubPushedDoesNotReopen(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Method == "GET" {
			writeJSONTo(w, 200, []any{})
			return
		}
		writeJSONTo(w, 201, map[string]any{"number": 8, "html_url": "https://github.com/me/rules/pull/8"})
	})
	if _, err := githubMR(context.Background(), ghRemote, "t", ghReq); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.requests() {
		if strings.Contains(r.URI, "state=closed") {
			t.Errorf("looked for closed pull requests after a push: %s", r.URI)
		}
	}
}

func TestGithubErrors(t *testing.T) {
	for _, status := range []int{401, 403} {
		newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, status, map[string]any{}) })
		if _, err := githubMR(context.Background(), ghRemote, "secret-token", ghReq); !errors.Is(err, errRejected) {
			t.Errorf("%d: err = %v, want errRejected", status, err)
		}
	}
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, 500, map[string]any{}) })
	_, err := githubMR(context.Background(), ghRemote, "secret-token", ghReq)
	if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("500: err = %v", err)
	}
}

func TestGithubDoesNotFollowRedirects(t *testing.T) {
	var hit bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	})
	if _, err := githubMR(context.Background(), ghRemote, "t", ghReq); err == nil {
		t.Error("a redirect was taken as an answer")
	}
	if hit {
		t.Error("the redirect was followed")
	}
}

func TestGithubNotOwnerName(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {})
	if _, err := githubMR(context.Background(), parseRemote("https://github.com/a/b/c"), "t", ghReq); err == nil {
		t.Error("a nested path was accepted")
	}
	if len(s.requests()) != 0 {
		t.Error("a request was sent")
	}
}

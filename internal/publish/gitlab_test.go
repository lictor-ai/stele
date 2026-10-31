package publish

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

var glRemote = parseRemote("git@gitlab.com:a/b/c.git")

func TestGitlabFindsOpen(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		writeJSONTo(w, 200, []map[string]any{{"iid": 3, "source_project_id": 1, "target_project_id": 1, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/3"}})
	})
	got, err := gitlabMR(context.Background(), glRemote, "t", ghReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "found" || got.Link != "https://gitlab.com/a/b/c/-/merge_requests/3" {
		t.Errorf("got %+v", got)
	}
	uri := s.requests()[0].URI
	for _, want := range []string{"/api/v4/projects/a%2Fb%2Fc/merge_requests?", "state=opened", "source_branch=stele%2Fpublish%2Fstele.md", "target_branch=main", "per_page=100"} {
		if !strings.Contains(uri, want) {
			t.Errorf("uri %s lacks %s", uri, want)
		}
	}
	if s.requests()[0].Auth != "Bearer t" {
		t.Errorf("auth = %q", s.requests()[0].Auth)
	}
}

func TestGitlabOpens(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Method == "GET" {
			writeJSONTo(w, 200, []any{})
			return
		}
		writeJSONTo(w, 201, map[string]any{"iid": 4, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/4"})
	})
	got, err := gitlabMR(context.Background(), glRemote, "t", ghReq)
	if err != nil || got.Action != "opened" {
		t.Fatalf("got %+v, %v", got, err)
	}
	post := s.requests()[1]
	if post.Method != "POST" || !strings.Contains(post.Body, `"source_branch":"stele/publish/stele.md"`) || !strings.Contains(post.Body, `"target_branch":"main"`) {
		t.Errorf("post = %+v", post)
	}
}

func TestGitlabConflictFindsOpen(t *testing.T) {
	gets := 0
	newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch r.Method {
		case "GET":
			gets++
			if gets == 1 {
				writeJSONTo(w, 200, []any{})
				return
			}
			writeJSONTo(w, 200, []map[string]any{{"iid": 5, "source_project_id": 1, "target_project_id": 1, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/5"}})
		case "POST":
			writeJSONTo(w, 409, map[string]any{"message": "already exists"})
		}
	})
	got, err := gitlabMR(context.Background(), glRemote, "t", ghReq)
	if err != nil || got.Action != "found" {
		t.Errorf("got %+v, %v; want found", got, err)
	}
}

// A 409 when one is already open from the branch, into an old base say: that
// one is the answer, found by its source branch alone.
func TestGitlabConflictIntoAnotherBase(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case r.Method == "POST":
			writeJSONTo(w, 409, map[string]any{"message": "already exists"})
		case r.Method == "GET" && r.URL.Query().Get("target_branch") == "":
			writeJSONTo(w, 200, []map[string]any{{"iid": 6, "source_project_id": 1, "target_project_id": 1, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/6"}})
		default:
			writeJSONTo(w, 200, []any{})
		}
	})
	got, err := gitlabMR(context.Background(), glRemote, "t", ghReq)
	if err != nil || got.Action != "found" || got.Link != "https://gitlab.com/a/b/c/-/merge_requests/6" {
		t.Fatalf("got %+v, %v; want merge request 6 found", got, err)
	}
	last := s.requests()[len(s.requests())-1]
	if !strings.Contains(last.URI, "source_branch=") || strings.Contains(last.URI, "target_branch=") {
		t.Errorf("last request %s, want the source branch filter alone", last.URI)
	}
}

func TestGitlabReopens(t *testing.T) {
	s := newAPI(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.RawQuery, "state=opened"):
			writeJSONTo(w, 200, []any{})
		case r.Method == "GET":
			writeJSONTo(w, 200, []map[string]any{{"iid": 7, "source_project_id": 1, "target_project_id": 1, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/7"}})
		case r.Method == "PUT":
			writeJSONTo(w, 200, map[string]any{"iid": 7, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/7"})
		}
	})
	req := ghReq
	req.Pushed = false
	got, err := gitlabMR(context.Background(), glRemote, "t", req)
	if err != nil || got.Action != "reopened" {
		t.Fatalf("got %+v, %v", got, err)
	}
	last := s.requests()[len(s.requests())-1]
	if !strings.HasSuffix(last.URI, "/merge_requests/7") || !strings.Contains(last.Body, `"state_event":"reopen"`) {
		t.Errorf("last = %+v", last)
	}
}

func TestGitlabNotFoundIsRejected(t *testing.T) {
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, 404, map[string]any{}) })
	if _, err := gitlabMR(context.Background(), glRemote, "t", ghReq); !errors.Is(err, errRejected) {
		t.Errorf("err = %v, want errRejected", err)
	}
}

func TestGitlabReopenFailureOpensNew(t *testing.T) {
	createStatus := 201
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.Method == "GET" && r.URL.Query().Get("state") == "opened":
			writeJSONTo(w, 200, []any{})
		case r.Method == "GET":
			writeJSONTo(w, 200, []map[string]any{{"iid": 7, "source_project_id": 1, "target_project_id": 1, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/7"}})
		case r.Method == "PUT":
			writeJSONTo(w, 500, map[string]any{})
		case r.Method == "POST":
			writeJSONTo(w, createStatus, map[string]any{"iid": 8, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/8"})
		}
	})
	req := ghReq
	req.Pushed = false
	if got, err := gitlabMR(context.Background(), glRemote, "t", req); err != nil || got.Action != "opened" {
		t.Errorf("got %+v, %v; want opened", got, err)
	}
	createStatus = 502
	_, err := gitlabMR(context.Background(), glRemote, "t", req)
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want both the create and the reopen failure", err)
	}
}

func TestGitlabErrors(t *testing.T) {
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, 401, map[string]any{}) })
	if _, err := gitlabMR(context.Background(), glRemote, "t", ghReq); !errors.Is(err, errRejected) {
		t.Errorf("401: err = %v", err)
	}
	newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) { writeJSONTo(w, 502, map[string]any{}) })
	if _, err := gitlabMR(context.Background(), glRemote, "t", ghReq); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("502: err = %v", err)
	}
}

// The source_branch filter also matches merge requests from forks, which
// anyone can open under the publish branch's name: they are never ours to
// report or reopen.
func TestGitlabIgnoresForks(t *testing.T) {
	const ownLink, forkLink = "https://gitlab.com/a/b/c/-/merge_requests/2", "https://gitlab.com/a/b/c/-/merge_requests/9"
	fork := map[string]any{"iid": 9, "web_url": forkLink, "source_project_id": 99, "target_project_id": 1}
	own := map[string]any{"iid": 2, "web_url": ownLink, "source_project_id": 1, "target_project_id": 1}
	bare := map[string]any{"iid": 9, "web_url": forkLink}
	created := map[string]any{"iid": 8, "web_url": "https://gitlab.com/a/b/c/-/merge_requests/8"}

	cases := []struct {
		name     string
		opened   []map[string]any
		closed   []map[string]any
		fallback []map[string]any // the list after a 409; nil is no 409
		pushed   bool
		want     string // the action, or "" for an error
		link     string
	}{
		{name: "open fork", opened: []map[string]any{fork}, pushed: true, want: "opened"},
		{name: "open fork before ours", opened: []map[string]any{fork, own}, pushed: true, want: "found", link: ownLink},
		{name: "open without ids", opened: []map[string]any{bare}, pushed: true, want: "opened"},
		{name: "closed fork", closed: []map[string]any{fork}, want: "opened"},
		{name: "closed fork before ours", closed: []map[string]any{fork, own}, want: "reopened", link: ownLink},
		{name: "conflict with a fork only", fallback: []map[string]any{fork}, pushed: true},
		{name: "conflict with a fork and ours", fallback: []map[string]any{fork, own}, pushed: true, want: "found", link: ownLink},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newAPI(t, func(w http.ResponseWriter, r *http.Request, _ string) {
				q := r.URL.Query()
				switch {
				case r.Method == "GET" && q.Get("target_branch") == "":
					writeJSONTo(w, 200, c.fallback)
				case r.Method == "GET" && q.Get("state") == "opened":
					writeJSONTo(w, 200, append([]map[string]any{}, c.opened...))
				case r.Method == "GET":
					writeJSONTo(w, 200, append([]map[string]any{}, c.closed...))
				case r.Method == "PUT":
					writeJSONTo(w, 200, own)
				case c.fallback != nil:
					writeJSONTo(w, 409, map[string]any{"message": "already exists"})
				default:
					writeJSONTo(w, 201, created)
				}
			})
			req := ghReq
			req.Pushed = c.pushed
			got, err := gitlabMR(context.Background(), glRemote, "t", req)
			if c.want == "" {
				if err == nil {
					t.Fatalf("got %+v, want an error", got)
				}
			} else if err != nil || got.Action != c.want {
				t.Fatalf("got %+v, %v; want %s", got, err, c.want)
			}
			if c.link != "" && got.Link != c.link {
				t.Errorf("link = %s, want %s", got.Link, c.link)
			}
			for _, r := range s.requests() {
				if r.Method == "PUT" && !strings.HasSuffix(r.URI, "/merge_requests/2") {
					t.Errorf("reopened %s, want only ours", r.URI)
				}
			}
		})
	}
}

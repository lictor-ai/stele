package publish

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type gitlabMergeRequest struct {
	IID             int    `json:"iid"`
	WebURL          string `json:"web_url"`
	SourceProjectID int    `json:"source_project_id"`
	TargetProjectID int    `json:"target_project_id"`
}

// ownMR reports whether m comes from a branch of the project it targets. The
// source_branch filter also matches forks, and anyone can open a merge request
// from a fork under the publish branch's name.
func ownMR(m gitlabMergeRequest) bool {
	return m.SourceProjectID != 0 && m.SourceProjectID == m.TargetProjectID
}

// gitlabMR finds the open merge request from the publish branch, or reopens
// a closed one when nothing new was pushed, or opens one. A 404 on the project
// or on the first list is a login that cannot see the project: errRejected.
func gitlabMR(ctx context.Context, r remote, token string, req mrRequest) (mrResult, error) {
	if !r.gitlabProject() {
		return mrResult{}, fmt.Errorf("gitlab.com/%s is not a group/name project", r.Project)
	}
	// The project id is its path with every '/' encoded as %2F.
	project := gitlabAPI + "/projects/" + url.PathEscape(r.Project)
	var p struct {
		ID int `json:"id"`
	}
	if _, err := call(ctx, "GitLab", http.MethodGet, project, token, nil, nil, &p); err != nil {
		return mrResult{}, notFoundRejects(err)
	}
	if p.ID == 0 {
		return mrResult{}, fmt.Errorf("GitLab gave no id for %s", r.Project)
	}
	mrs := project + "/merge_requests"
	list := func(state, target string) ([]gitlabMergeRequest, error) {
		// The API drops forks here, so ours is on the first page however many
		// there are; ownMR then holds even if it did not.
		q := url.Values{"state": {state}, "source_branch": {req.Branch}, "source_project_id": {strconv.Itoa(p.ID)}}
		if target != "" {
			q.Set("target_branch", target)
		}
		var found []gitlabMergeRequest
		if _, err := call(ctx, "GitLab", http.MethodGet, mrs+"?"+q.Encode(), token, nil, nil, &found); err != nil {
			return nil, err
		}
		var own []gitlabMergeRequest
		for _, m := range found {
			if ownMR(m) {
				own = append(own, m)
			}
		}
		return own, nil
	}

	open, err := list("opened", req.Base)
	if err != nil {
		return mrResult{}, notFoundRejects(err)
	}
	if len(open) > 0 {
		return mrResult{Link: open[0].WebURL, Action: "found"}, nil
	}
	var reopenErr error
	if !req.Pushed {
		closed, err := list("closed", req.Base)
		if err != nil {
			return mrResult{}, err
		}
		if len(closed) > 0 {
			var out gitlabMergeRequest
			_, reopenErr = call(ctx, "GitLab", http.MethodPut, fmt.Sprintf("%s/%d", mrs, closed[0].IID), token, nil, map[string]string{"state_event": "reopen"}, &out)
			if reopenErr == nil {
				return mrResult{Link: out.WebURL, Action: "reopened"}, nil
			}
		}
	}
	var out gitlabMergeRequest
	body := map[string]string{"source_branch": req.Branch, "target_branch": req.Base, "title": req.Title, "description": req.Body}
	_, err = call(ctx, "GitLab", http.MethodPost, mrs, token, nil, body, &out)
	if status(err) == http.StatusConflict {
		// One is open from this branch, into another base say, or opened
		// meanwhile: that one is the answer.
		if open, lerr := list("opened", ""); lerr == nil && len(open) > 0 {
			return mrResult{Link: open[0].WebURL, Action: "found"}, nil
		}
	}
	if err != nil {
		return mrResult{}, alsoReopen(err, reopenErr)
	}
	return mrResult{Link: out.WebURL, Action: "opened"}, nil
}

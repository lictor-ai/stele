package publish

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

type gitlabMergeRequest struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
}

// gitlabMR finds the open merge request from the publish branch, or reopens
// a closed one when nothing new was pushed, or opens one.
func gitlabMR(ctx context.Context, r remote, token string, req mrRequest) (mrResult, error) {
	if !r.gitlabProject() {
		return mrResult{}, fmt.Errorf("gitlab.com/%s is not a group/name project", r.Project)
	}
	// The project id is its path with every '/' encoded as %2F.
	mrs := gitlabAPI + "/projects/" + url.PathEscape(r.Project) + "/merge_requests"
	list := func(state string) ([]gitlabMergeRequest, error) {
		q := url.Values{"state": {state}, "source_branch": {req.Branch}, "target_branch": {req.Base}}
		var out []gitlabMergeRequest
		_, err := call(ctx, "GitLab", http.MethodGet, mrs+"?"+q.Encode(), token, nil, nil, &out)
		return out, err
	}
	findOpen := func() (mrResult, bool, error) {
		open, err := list("opened")
		if err != nil || len(open) == 0 {
			return mrResult{}, false, err
		}
		return mrResult{Link: open[0].WebURL, Action: "found"}, true, nil
	}

	if res, ok, err := findOpen(); err != nil || ok {
		return res, err
	}
	if !req.Pushed {
		closed, err := list("closed")
		if err != nil {
			return mrResult{}, err
		}
		if len(closed) > 0 {
			var out gitlabMergeRequest
			_, err := call(ctx, "GitLab", http.MethodPut, fmt.Sprintf("%s/%d", mrs, closed[0].IID), token, nil, map[string]string{"state_event": "reopen"}, &out)
			if err == nil {
				return mrResult{Link: out.WebURL, Action: "reopened"}, nil
			}
			var ae *apiError
			if !errors.As(err, &ae) {
				return mrResult{}, err
			}
		}
	}
	var out gitlabMergeRequest
	body := map[string]string{"source_branch": req.Branch, "target_branch": req.Base, "title": req.Title, "description": req.Body}
	_, err := call(ctx, "GitLab", http.MethodPost, mrs, token, nil, body, &out)
	var ae *apiError
	if errors.As(err, &ae) && ae.status == http.StatusConflict {
		// Opened meanwhile: find it.
		if res, ok, err := findOpen(); err != nil || ok {
			return res, err
		}
	}
	if err != nil {
		return mrResult{}, err
	}
	return mrResult{Link: out.WebURL, Action: "opened"}, nil
}

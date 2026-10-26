package publish

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

type gitlabMergeRequest struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
}

// gitlabMR finds the open merge request from the publish branch, or reopens
// a closed one when nothing new was pushed, or opens one. A 404 on the first
// call is a login that cannot see the project: errRejected.
func gitlabMR(ctx context.Context, r remote, token string, req mrRequest) (mrResult, error) {
	if !r.gitlabProject() {
		return mrResult{}, fmt.Errorf("gitlab.com/%s is not a group/name project", r.Project)
	}
	// The project id is its path with every '/' encoded as %2F.
	mrs := gitlabAPI + "/projects/" + url.PathEscape(r.Project) + "/merge_requests"
	list := func(state, target string) ([]gitlabMergeRequest, error) {
		q := url.Values{"state": {state}, "source_branch": {req.Branch}}
		if target != "" {
			q.Set("target_branch", target)
		}
		var out []gitlabMergeRequest
		_, err := call(ctx, "GitLab", http.MethodGet, mrs+"?"+q.Encode(), token, nil, nil, &out)
		return out, err
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

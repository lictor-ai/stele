package publish

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// githubHeaders go with every GitHub API call.
var githubHeaders = map[string]string{
	"Accept":               "application/vnd.github+json",
	"X-GitHub-Api-Version": "2022-11-28",
}

type githubPull struct {
	Number   int     `json:"number"`
	HTMLURL  string  `json:"html_url"`
	MergedAt *string `json:"merged_at"`
}

// githubMR finds the open pull request from the publish branch, or reopens a
// closed one when nothing new was pushed, or opens one.
func githubMR(ctx context.Context, r remote, token string, req mrRequest) (mrResult, error) {
	owner, name, ok := r.githubRepo()
	if !ok {
		return mrResult{}, fmt.Errorf("github.com/%s is not an owner/name repository", r.Project)
	}
	pulls := githubAPI + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls"
	list := func(state string) ([]githubPull, error) {
		q := url.Values{"state": {state}, "head": {owner + ":" + req.Branch}, "base": {req.Base}}
		var out []githubPull
		_, err := call(ctx, "GitHub", http.MethodGet, pulls+"?"+q.Encode(), token, githubHeaders, nil, &out)
		return out, err
	}

	open, err := list("open")
	if err != nil {
		return mrResult{}, err
	}
	if len(open) > 0 {
		return mrResult{Link: open[0].HTMLURL, Action: "found"}, nil
	}
	if !req.Pushed {
		closed, err := list("closed")
		if err != nil {
			return mrResult{}, err
		}
		for _, p := range closed {
			if p.MergedAt != nil {
				continue
			}
			var out githubPull
			_, err := call(ctx, "GitHub", http.MethodPatch, fmt.Sprintf("%s/%d", pulls, p.Number), token, githubHeaders, map[string]string{"state": "open"}, &out)
			if err == nil {
				return mrResult{Link: out.HTMLURL, Action: "reopened"}, nil
			}
			var ae *apiError
			if !errors.As(err, &ae) || ae.status != http.StatusUnprocessableEntity {
				return mrResult{}, err
			}
			break // GitHub cannot reopen it: open a new one
		}
	}
	var out githubPull
	body := map[string]string{"title": req.Title, "head": req.Branch, "base": req.Base, "body": req.Body}
	if _, err := call(ctx, "GitHub", http.MethodPost, pulls, token, githubHeaders, body, &out); err != nil {
		return mrResult{}, err
	}
	return mrResult{Link: out.HTMLURL, Action: "opened"}, nil
}

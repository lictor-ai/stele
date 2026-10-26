package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// mergeRequest opens the merge request for res on github.com or gitlab.com,
// or finds or reopens it, with the first login the host accepts. When it
// cannot, res.Page is the host's page to open one by hand and res.Why says
// why. It never fails Run: the branch is already pushed.
func mergeRequest(ctx context.Context, g gitCmd, root, dir string, r remote, c committed, res *Result) {
	res.Page = prefilledPage(r, res.Base, res.Branch)
	var open func(context.Context, remote, string, mrRequest) (mrResult, error)
	switch r.Kind {
	case github:
		if _, _, ok := r.githubRepo(); !ok {
			res.Why = fmt.Sprintf("github.com/%s is not an owner/name repository", r.Project)
			return
		}
		open = githubMR
	case gitlab:
		if !r.gitlabProject() {
			res.Why = fmt.Sprintf("gitlab.com/%s is not a group/name project", r.Project)
			return
		}
		open = gitlabMR
	default:
		return
	}
	req := mrRequest{
		Base:   res.Base,
		Branch: res.Branch,
		Title:  subject(res.Path),
		Body:   fmt.Sprintf("Published by stele publish from project %s at commit %s.", c.Project, c.Head),
		Pushed: res.Pushed,
	}
	var tried, rejected []string
	for _, src := range tokenSources(g, root, dir, r) {
		token := src.get(ctx)
		if token == "" {
			continue
		}
		tried = append(tried, src.name)
		mr, err := open(ctx, r, token, req)
		if errors.Is(err, errRejected) {
			rejected = append(rejected, src.name)
			continue
		}
		if err != nil {
			res.Why = err.Error()
			return
		}
		if !sameHostHTTPS(mr.Link, r.Host) {
			res.Why = r.Host + " answered with no usable link"
			return
		}
		res.MR, res.MRAction, res.Page = mr.Link, mr.Action, ""
		return
	}
	if len(tried) == 0 {
		names := []string{"gh", "git's stored credential", "GITHUB_TOKEN"}
		if r.Kind == gitlab {
			names = []string{"glab", "git's stored credential", "GITLAB_TOKEN"}
		}
		res.Why = "no login found for " + r.Host + " (" + strings.Join(names, ", ") + ")"
		return
	}
	res.Why = r.Host + " rejected every login found (" + strings.Join(rejected, ", ") + ")"
}

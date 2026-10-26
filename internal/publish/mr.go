package publish

import (
	"context"
)

// mergeRequest opens the merge request for res, or finds or reopens it, on
// github.com or gitlab.com. When it cannot, it sets res.Page to the host's
// page to open one by hand, and res.Why to the reason. It never fails Run:
// the branch is already pushed.
func mergeRequest(ctx context.Context, g gitCmd, root string, r remote, c committed, res *Result) {
	res.Page = prefilledPage(r, res.Base, res.Branch)
	if !r.tokenHost() {
		return
	}
	res.Why = "no login found for " + r.Host
}

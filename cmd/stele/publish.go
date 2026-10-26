package main

import (
	"context"
	"fmt"
	"io"

	"github.com/lictor-ai/stele/internal/publish"
)

// publishRun and openLink are stele publish's work and its browser opener.
// Tests replace them.
var (
	publishRun = publish.Run
	openLink   = publish.Open
)

// runPublish sends the committed stele.md to the repository named under
// "publish" in stele.json, as a merge request (D64). It exits 1 when nothing
// was pushed because of an error, and 0 otherwise, including when the merge
// request could not be opened: the branch is pushed and the page to open it
// is printed.
func runPublish(args []string, stdout, stderr io.Writer) int {
	root, ok := setup("publish", args, stderr)
	if !ok {
		return exitCantRun
	}
	res, err := publishRun(context.Background(), root)
	if err != nil {
		fmt.Fprintf(stderr, "stele publish: %s\n", publish.Clean(err.Error()))
		return exitFailed
	}
	c := publish.Clean
	if res.Unchanged {
		fmt.Fprintf(stdout, "%s on %s in %s is already this stele.md: nothing to publish\n", c(res.Path), c(res.Base), c(res.Repo))
		return exitOK
	}
	if res.Pushed {
		fmt.Fprintf(stdout, "pushed branch %s to %s\n", c(res.Branch), c(res.Repo))
	} else {
		fmt.Fprintf(stdout, "branch %s in %s already has this stele.md\n", c(res.Branch), c(res.Repo))
	}
	link := ""
	switch {
	case res.MR != "":
		fmt.Fprintf(stdout, "merge request %s: %s\n", res.MRAction, c(res.MR))
		link = res.MR
	case res.Page != "":
		fmt.Fprintf(stdout, "no merge request opened: %s\nopen it here: %s\n", c(res.Why), c(res.Page))
		link = res.Page
	default:
		if res.Why != "" {
			fmt.Fprintf(stdout, "no merge request opened: %s\n", c(res.Why))
		}
		fmt.Fprintf(stdout, "open a merge request in %s from branch %s into %s\n", c(res.Repo), c(res.Branch), c(res.Base))
	}
	if link != "" {
		_ = openLink(root, link) // the link is printed: a browser that does not open is no failure
	}
	return exitOK
}

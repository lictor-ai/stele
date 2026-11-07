package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/push"
	"github.com/lictor-ai/stele/internal/sign"
)

// runPush sends the log's new lines to every sink stele.json lists (D83).
// It exits 0 when every sink took everything, or there was nothing to send
// or no sink; 1 when a sink took fewer lines than it was sent; 2 when stele
// or a sink could not run.
func runPush(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stele push", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return exitCantRun
	}
	cantRun := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "stele push: "+format+"\n", a...)
		return exitCantRun
	}
	if flags.NArg() > 0 {
		return cantRun("takes no arguments, got %q", flags.Arg(0))
	}
	root, err := findRoot(".")
	if err != nil {
		return cantRun("%v", err)
	}
	ss, err := listedSinks(root, stderr)
	if err != nil {
		return cantRun("%v", err)
	}
	if len(ss) == 0 {
		fmt.Fprintln(stdout, "no sink in stele.json: nothing sent")
		return exitOK
	}
	key, err := loadExistingKey()
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(stdout, "no log on this machine yet: nothing sent")
		return exitOK
	}
	if err != nil {
		return cantRun("%v", err)
	}
	return pushLog(root, key, ss, stdout, stderr, "stele push")
}

// pushLog pushes root's log to ss and prints a line per sink. It returns the
// exit code: 0 all sent, 1 a sink took fewer, 2 a sink or the push failed.
func pushLog(root string, key sign.Key, ss []*plugin.Sink, stdout, stderr io.Writer, cmd string) int {
	f, err := lock.Load(root)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return exitCantRun
	}
	if f.Project == "" {
		fmt.Fprintf(stderr, "%s: stele.lock has no project id yet: run stele compile\n", cmd)
		return exitCantRun
	}
	ctx, stop := interruptible()
	defer stop()
	res, err := push.Run(ctx, root, key, f.Project, pushTargets(ss), now)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return exitCantRun
	}
	return printPush(stdout, stderr, cmd, res)
}

func pushTargets(ss []*plugin.Sink) []push.Target {
	out := make([]push.Target, len(ss))
	for i, s := range ss {
		out[i] = push.Target{Name: s.Name, Push: s.Push}
	}
	return out
}

// printPush says what each sink got, and returns the exit code.
func printPush(stdout, stderr io.Writer, cmd string, res push.Result) int {
	code := exitOK
	for _, r := range res.Sinks {
		line := fmt.Sprintf("%s: sent %s", r.Name, plural(r.Sent, "line"))
		var skipped []string
		if r.Unverified > 0 {
			skipped = append(skipped, fmt.Sprintf("%d not signed on this machine", r.Unverified))
		}
		if r.OtherProject > 0 {
			skipped = append(skipped, fmt.Sprintf("%d of another project", r.OtherProject))
		}
		if len(skipped) > 0 {
			line += " (skipped: " + strings.Join(skipped, ", ") + ")"
		}
		fmt.Fprintln(stdout, line)
		if r.Gap {
			fmt.Fprintf(stdout, "%s: some lines rotated out of the log before they were pushed\n", r.Name)
		}
		switch {
		case r.Err != nil:
			fmt.Fprintf(stderr, "%s: %s: %v\n", cmd, r.Name, r.Err)
			if r.Pending > 0 {
				fmt.Fprintf(stderr, "%s: %s: %s wait for the next push\n", cmd, r.Name, plural(r.Pending, "line"))
			}
			code = exitCantRun
		case r.Partial:
			fmt.Fprintf(stdout, "%s: took %d of the lines sent; %s wait for the next push\n", r.Name, r.Sent, plural(r.Pending, "line"))
			if code == exitOK {
				code = exitFailed
			}
		}
	}
	return code
}

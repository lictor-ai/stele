package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/sign"
	"github.com/lictor-ai/stele/internal/status"
)

// runStatus describes the install (D85). It writes nothing. Unless
// --offline it asks each listed plugin its version and each external source
// its fingerprint, so it may start plugins; it is never on the hook's path.
// It exits 0 whenever it could describe the install, a missing plugin
// included, and 2 when it could not.
func runStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stele status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output `format`: text or json")
	offline := flags.Bool("offline", false, "start no plugin: versions and external sources' freshness are unknown")
	if err := flags.Parse(args); err != nil {
		return exitCantRun
	}
	cantRun := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "stele status: "+format+"\n", a...)
		return exitCantRun
	}
	if *format != "text" && *format != "json" {
		return cantRun("--format is text or json, not %q", *format)
	}
	if flags.NArg() > 0 {
		return cantRun("takes no arguments, got %q", flags.Arg(0))
	}
	root, err := findRoot(".")
	if err != nil {
		return cantRun("%v", err)
	}
	items, run, err := inventory(root, *offline, stderr)
	if err != nil {
		return cantRun("%v", err)
	}
	in := status.Input{Version: binaryVersion(), Agents: agents, Items: items, Run: run}
	if key, err := loadExistingKey(); err == nil {
		in.Key = key
	}
	in.Sinks = statusSinks(root, in.Key)
	ctx, stop := interruptible()
	defer stop()
	st, err := status.Collect(ctx, root, in)
	if err != nil {
		return cantRun("%v", err)
	}
	if *format == "json" {
		data, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return cantRun("%v", err)
		}
		fmt.Fprintf(stdout, "%s\n", data)
		return exitOK
	}
	printStatus(stdout, root, st)
	return exitOK
}

// printStatus is the text form of st: a short summary, one section each.
func printStatus(w io.Writer, root string, st status.Status) {
	project := st.Project
	if project == "" {
		project = "none yet"
	}
	fmt.Fprintf(w, "stele %s in %s, project %s\n", st.Version, showPath(root), project)

	fmt.Fprintln(w, "\nagents")
	for _, a := range st.Agents {
		switch {
		case !a.Hooks:
			fmt.Fprintf(w, "  %-12s not set up\n", a.Name)
		default:
			stamp := a.Installed
			if stamp == "" {
				stamp = "no stamp"
			}
			rules := "no rules file"
			if a.RulesPresent {
				rules = a.RulesFile
			}
			fmt.Fprintf(w, "  %-12s hooks in %s (%s), remind %s, check %s, %s\n",
				a.Name, a.SettingsFile, stamp, onOff(a.Remind), onOff(a.Check), rules)
		}
	}

	fmt.Fprintln(w, "\nplugins")
	for _, p := range st.Plugins {
		var state string
		switch {
		case p.Builtin && p.Error == "":
			state = "built in"
		case p.Found != nil && !*p.Found:
			state = plugin.Exe(p.Kind, p.Name) + " not found on PATH"
		case p.Error != "":
			state = p.Error
		case p.Found == nil:
			state = "not looked up (--offline)"
		default:
			state = p.Version
		}
		fmt.Fprintf(w, "  %-8s %-30s %s\n", p.Kind, p.Name, state)
	}

	fmt.Fprintln(w, "\nsources")
	if len(st.Sources) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, s := range st.Sources {
		fmt.Fprintf(w, "  %-38s %s, %s\n", s.Name, plural(s.Rules, "rule"), freshText(s.Fresh))
	}
	idx := "none"
	switch {
	case st.Index.Present && st.Index.Valid:
		idx = freshText(st.Index.Fresh)
	case st.Index.Present:
		idx = "present, not checked"
	}
	fmt.Fprintf(w, "\nindex: %s\n", idx)

	if len(st.Sinks) > 0 {
		fmt.Fprintln(w, "\nsinks")
		for _, s := range st.Sinks {
			line := fmt.Sprintf("%d lines to send", s.Pending)
			if s.Error != "" {
				line = s.Error
			}
			if s.LastPush != "" {
				line += ", last push " + s.LastPush
			}
			fmt.Fprintf(w, "  %-12s %s\n", s.Name, line)
		}
	}
	if len(st.Notes) > 0 {
		fmt.Fprintln(w)
		for _, n := range st.Notes {
			fmt.Fprintln(w, "note: "+strings.TrimSpace(n))
		}
	}
}

// statusSinks is the sinks stele.json lists and what waits for each; none
// until stele push exists.
func statusSinks(string, sign.Key) []status.Sink { return nil }

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func freshText(f *bool) string {
	switch {
	case f == nil:
		return "freshness unknown"
	case *f:
		return "fresh"
	}
	return "stale: run stele compile"
}

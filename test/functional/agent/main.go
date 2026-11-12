// Command agent runs the agent A/B eval: the same coding tasks given to
// headless Claude Code with no rules, with the rules in CLAUDE.md, and with
// stele's hooks, then graded with one yardstick. It measures what stele adds
// in time, tokens and cost, and what it buys in compliance.
//
//	go run ./test/functional/agent -reps 2
//
// It spends real money: see README.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// arms are the setups compared, in report order. agentsmd is claudemd for
// Codex: the rules pasted into AGENTS.md. stele-tools is stele with the agent
// told to change files only with its file tools, which the hooks see; it is
// a diagnostic, not run by default.
var arms = []string{"none", "claudemd", "agentsmd", "stele", "stele-tools"}

// evalScenarios are the published evaluation's scenarios (-eval,
// docs/eval.md): every scenario but the probes, which were written to find
// where a rules file slips.
var evalScenarios = []string{
	"big-billing-refactor", "big-log-analyzer", "big-todo-service",
	"edit-legacy", "go-logging", "migrations", "multi-file-python",
	"no-secrets", "subproject",
}

type config struct {
	agent                string // claude-code or codex
	claude, stele, model string
	codex                []string // the command that starts Codex
	effort               string   // Codex's model_reasoning_effort
	codexHome            string   // the eval's CODEX_HOME
	goDir                string   // Go's cache and temp files, writable in Codex's sandbox
	timeout              time.Duration
	scenarioDir, outDir  string
	streamDir            string
	keep                 bool // keep each run's temporary tree
	rtk                  bool // add the user's rtk hook, and nothing else of theirs
}

func main() {
	var (
		scenarios = flag.String("scenarios", "", "comma-separated scenarios to run (default: all)")
		armList   = flag.String("arms", "", "comma-separated arms: none, claudemd, agentsmd, stele, stele-tools (default: none,claudemd,stele, or none,agentsmd,stele for Codex)")
		reps      = flag.Int("reps", 1, "runs per scenario and arm")
		parallel  = flag.Int("parallel", 3, "runs at once (default 2 with -eval)")
		baseline  = flag.String("baseline", "", "comma-separated results/*.jsonl whose runs of the arms not run now join the summary")
		evalSet   = flag.Bool("eval", false, "the published evaluation (docs/eval.md): its 9 scenarios, arms claudemd and stele (agentsmd and stele for Codex)")
		out       = flag.String("out", "", "a folder under results/ for this eval's files (default: results/ itself)")
		cfg       config
	)
	flag.StringVar(&cfg.claude, "claude", "", "claude binary (default: newest VS Code extension's, else claude on PATH)")
	flag.StringVar(&cfg.stele, "stele", "", "stele binary (default: built from this module)")
	flag.StringVar(&cfg.agent, "agent", "claude-code", "agent: claude-code or codex")
	codexFlag := flag.String("codex", "", "codex binary, or its codex.js (default: the npm package's codex.js, else codex on PATH)")
	flag.StringVar(&cfg.effort, "effort", "medium", "Codex's model_reasoning_effort")
	flag.StringVar(&cfg.model, "model", "", "model the agent runs on (default: claude-sonnet-5-5, or gpt-6-luna for Codex)")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Minute, "timeout per run")
	flag.BoolVar(&cfg.keep, "keep", false, "keep each run's temporary tree (default: only its diff, beside the stream)")
	flag.BoolVar(&cfg.rtk, "rtk", false, "add the rtk hook from the user's settings (only it) to every arm")
	flag.Parse()

	here, err := sourceDir()
	if err != nil {
		fail(err)
	}
	cfg.scenarioDir = filepath.Join(here, "scenarios")
	if *out != "" && (filepath.Base(*out) != *out || *out == "." || *out == "..") {
		fail(fmt.Errorf("-out %q: give a folder name, not a path", *out))
	}
	cfg.outDir = filepath.Join(here, "results", *out)
	if *evalSet {
		// Fewer agents at once: 4 ran this machine short of memory (results,
		// experiment 7).
		set := false
		flag.Visit(func(f *flag.Flag) { set = set || f.Name == "parallel" })
		if !set {
			*parallel = 2
		}
	}
	if *evalSet && *scenarios == "" {
		*scenarios = strings.Join(evalScenarios, ",")
	}
	switch cfg.agent {
	case "claude-code":
		if cfg.model == "" {
			cfg.model = "claude-sonnet-5-5"
		}
		if *armList == "" && *evalSet {
			*armList = "claudemd,stele"
		}
		if *armList == "" {
			*armList = "none,claudemd,stele"
		}
		if cfg.claude == "" {
			cfg.claude = findClaude()
		}
	case "codex":
		if cfg.model == "" {
			cfg.model = "gpt-6-luna"
		}
		if *armList == "" && *evalSet {
			*armList = "agentsmd,stele"
		}
		if *armList == "" {
			*armList = "none,agentsmd,stele"
		}
		cfg.codex = findCodex()
		if *codexFlag != "" {
			cfg.codex = codexCommand(*codexFlag)
		}
		if cfg.codexHome, err = newCodexHome(); err != nil {
			fail(err)
		}
		cleanup = func() { os.RemoveAll(cfg.codexHome) }
		defer cleanup()
		cfg.goDir = filepath.Join(cfg.codexHome, "go")
		for _, d := range []string{"cache", "tmp"} {
			if err := os.MkdirAll(filepath.Join(cfg.goDir, d), 0o755); err != nil {
				fail(err)
			}
		}
	default:
		fail(fmt.Errorf("unknown agent %q", cfg.agent))
	}
	var steleDesc string
	if cfg.stele != "" {
		steleDesc = cfg.stele + ", given with -stele: " + buildInfo(cfg.stele)
	}
	if cfg.stele == "" {
		root := filepath.Join(here, "..", "..", "..")
		tag, desc := steleSource(root)
		cfg.stele, err = buildStele(root, tag)
		if err != nil {
			fail(err)
		}
		steleDesc = "built from this module: " + desc
	}

	names := split(*scenarios)
	if len(names) == 0 {
		names = scenarioNames(cfg.scenarioDir)
	}
	for _, n := range names {
		if graders[n] == nil {
			fail(fmt.Errorf("unknown scenario %q", n))
		}
	}
	chosen := split(*armList)
	for _, a := range chosen {
		if !slices.Contains(arms, a) {
			fail(fmt.Errorf("unknown arm %q", a))
		}
	}

	stamp := time.Now().Format("20060102-150405")
	cfg.streamDir = filepath.Join(cfg.outDir, stamp)
	if err := os.MkdirAll(cfg.streamDir, 0o755); err != nil {
		fail(err)
	}
	jsonl, err := os.Create(filepath.Join(cfg.outDir, stamp+".jsonl"))
	if err != nil {
		fail(err)
	}
	defer jsonl.Close()

	type job struct {
		scenario, arm string
		rep           int
	}
	var jobs []job
	for rep := 1; rep <= *reps; rep++ {
		for _, n := range names {
			for _, a := range chosen {
				jobs = append(jobs, job{n, a, rep})
			}
		}
	}
	agentBin := "claude " + cfg.claude
	if cfg.agent == "codex" {
		agentBin = strings.Join(cfg.codex, " ") + ", effort " + cfg.effort
	}
	fmt.Fprintf(os.Stderr, "%d runs, agent %s, model %s, %s, stele %s (%s)\n", len(jobs), cfg.agent, cfg.model, agentBin, cfg.stele, steleDesc)

	var (
		mu      sync.Mutex
		results []Result
		wg      sync.WaitGroup
		sem     = make(chan struct{}, max(1, *parallel))
	)
	// The last check before results/: nothing written there may hold the
	// home directory, the account name or the account's emails and ids.
	guard := newScrubber("")
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := runOne(cfg, j.scenario, j.arm, j.rep)
			line, _ := json.Marshal(r)
			mu.Lock()
			defer mu.Unlock()
			if what := guard.leak(line); what != "" {
				fail(fmt.Errorf("%s-%s-%d: its result still holds %s after scrubbing: not written", r.Scenario, r.Arm, r.Rep, what))
			}
			results = append(results, r)
			jsonl.Write(append(line, '\n'))
			fmt.Fprintf(os.Stderr, "%-18s %-8s #%d  %6.1fs  $%.3f  compliant=%v task_ok=%v denies=%d blocks=%d %s\n",
				r.Scenario, r.Arm, r.Rep, float64(r.DurationMS)/1000, r.CostUSD, r.Compliant, r.TaskOK, r.Denies, r.Blocks, r.Error)
		}()
	}
	wg.Wait()

	// Arms that do not run stele need not run again for a new stele: their
	// earlier results stand in, for the arms and scenarios not run now.
	for _, f := range split(*baseline) {
		old, err := loadBaseline(f, cfg.scenarioDir, names, chosen, cfg.model, cfg.rtk)
		if err != nil {
			fail(err)
		}
		results = append(results, old...)
	}

	about := []string{
		"Agent: " + cfg.agent + ", " + homeless(agentBin) + ".",
		"stele: " + homeless(steleDesc) + ".",
		"Command: `go run ./test/functional/agent " + homeless(strings.Join(os.Args[1:], " ")) + "`.",
	}
	sum := string(guard.text([]byte(summary(results, cfg.model, about))))
	if what := guard.leak([]byte(sum)); what != "" {
		fail(fmt.Errorf("the summary still holds %s after scrubbing: not written", what))
	}
	if err := os.WriteFile(filepath.Join(cfg.outDir, stamp+".md"), []byte(sum), 0o644); err != nil {
		fail(err)
	}
	fmt.Print(sum)
}

// homeless replaces the user's home directory with ~, so a summary meant to
// be published names no local account. It replaces it only where a path
// ends or goes on (a separator), and never a home that is a root.
func homeless(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || filepath.Dir(home) == home {
		return s
	}
	re := regexp.MustCompile(caseFold + regexp.QuoteMeta(home) + `([\\/]|$|[^\w.-])`)
	return re.ReplaceAllString(s, "~$1")
}

// buildInfo says what a stele binary was built from: its module version,
// commit and -ldflags, from go version -m. stele has no version command.
func buildInfo(bin string) string {
	b, err := exec.Command("go", "version", "-m", bin).Output()
	if err != nil {
		return "no build info"
	}
	var parts []string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) >= 3 && f[0] == "mod":
			parts = append(parts, f[1]+" "+f[2])
		case len(f) >= 2 && f[0] == "build" && (strings.HasPrefix(f[1], "-ldflags=") || strings.HasPrefix(f[1], "vcs.revision=") || strings.HasPrefix(f[1], "vcs.modified=")):
			parts = append(parts, strings.Join(f[1:], " "))
		}
	}
	return strings.Join(parts, ", ")
}

// sourceDir is this package's directory, so the harness runs from any cwd.
func sourceDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate the harness source")
	}
	return filepath.Dir(file), nil
}

// findClaude returns the newest Claude Code binary bundled with the VS Code
// extension, or "claude" for the one on PATH.
func findClaude() string {
	home, _ := os.UserHomeDir()
	exe := "claude"
	if runtime.GOOS == "windows" {
		exe = "claude.exe"
	}
	dirs, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "anthropic.claude-code-*"))
	sort.Slice(dirs, func(i, j int) bool { return versionLess(dirs[i], dirs[j]) })
	for i := len(dirs) - 1; i >= 0; i-- {
		p := filepath.Join(dirs[i], "resources", "native-binary", exe)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "claude"
}

// versionLess orders extension directories by their dotted version.
func versionLess(a, b string) bool {
	num := func(s string) []int {
		s = strings.TrimPrefix(filepath.Base(s), "anthropic.claude-code-")
		var out []int
		for _, p := range strings.Split(strings.SplitN(s, "-", 2)[0], ".") {
			var n int
			fmt.Sscanf(p, "%d", &n)
			out = append(out, n)
		}
		return out
	}
	return slices.Compare(num(a), num(b)) < 0
}

// steleSource says which stele the module at root builds. When stele's own
// code (cmd/, internal/, go.mod, go.sum) is as it was at the latest tag, in
// the work tree too, tag is that tag, and the build carries it as a release
// build does (.goreleaser.yaml). Otherwise tag is "" and the build reports
// "devel".
func steleSource(root string) (tag, desc string) {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		b, err := cmd.Output()
		return strings.TrimSpace(string(b)), err
	}
	head, err := git("describe", "--tags", "--always", "--dirty")
	if err != nil {
		return "", "commit unknown"
	}
	last, err := git("describe", "--tags", "--abbrev=0")
	if err != nil {
		return "", head
	}
	// git diff --quiet exits 1 when the code differs from the tag; status
	// lists new files git does not track yet.
	_, diff := git("diff", "--quiet", last, "--", "cmd", "internal", "go.mod", "go.sum")
	untracked, err := git("status", "--porcelain", "--untracked-files=all", "--", "cmd", "internal", "go.mod", "go.sum")
	if diff != nil || err != nil || untracked != "" {
		return "", head + ", stele's code changed since " + last
	}
	return last, last + " (stele's code as tagged; the module is at " + head + ")"
}

// buildStele builds stele from the module at root into a temp directory,
// carrying tag as its version when tag is not "".
func buildStele(root, tag string) (string, error) {
	dir, err := os.MkdirTemp("", "stele-eval-bin-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "stele")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	args := []string{"build", "-o", out}
	if tag != "" {
		args = append(args, "-ldflags", "-X main.version="+tag)
	}
	cmd := exec.Command("go", append(args, "./cmd/stele")...)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building stele: %v\n%s", err, b)
	}
	return out, nil
}

func scenarioNames(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if e.IsDir() && graders[e.Name()] != nil {
			names = append(names, e.Name())
		}
	}
	return names
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cleanup removes what the eval made outside results/ (Codex's home, with
// its copy of the login), also when it fails.
var cleanup = func() {}

func fail(err error) {
	cleanup()
	fmt.Fprintln(os.Stderr, "agent eval:", err)
	os.Exit(2)
}

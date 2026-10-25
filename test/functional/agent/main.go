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
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// arms are the setups compared, in report order. stele-tools is stele with
// the agent told to change files only with its file tools, which the hooks
// see; it is a diagnostic, not run by default.
var arms = []string{"none", "claudemd", "stele", "stele-tools"}

type config struct {
	claude, stele, model string
	timeout              time.Duration
	scenarioDir, outDir  string
	streamDir            string
	keep                 bool // keep each run's temporary tree
	rtk                  bool // add the user's rtk hook, and nothing else of theirs
}

func main() {
	var (
		scenarios = flag.String("scenarios", "", "comma-separated scenarios to run (default: all)")
		armList   = flag.String("arms", "none,claudemd,stele", "comma-separated arms: none, claudemd, stele, stele-tools")
		reps      = flag.Int("reps", 1, "runs per scenario and arm")
		parallel  = flag.Int("parallel", 3, "runs at once")
		baseline  = flag.String("baseline", "", "comma-separated results/*.jsonl whose runs of the arms not run now join the summary")
		cfg       config
	)
	flag.StringVar(&cfg.claude, "claude", "", "claude binary (default: newest VS Code extension's, else claude on PATH)")
	flag.StringVar(&cfg.stele, "stele", "", "stele binary (default: built from this module)")
	flag.StringVar(&cfg.model, "model", "claude-sonnet-5-5", "model the agent runs on")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Minute, "timeout per run")
	flag.BoolVar(&cfg.keep, "keep", false, "keep each run's temporary tree (default: only its diff, beside the stream)")
	flag.BoolVar(&cfg.rtk, "rtk", false, "add the rtk hook from the user's settings (only it) to every arm")
	flag.Parse()

	here, err := sourceDir()
	if err != nil {
		fail(err)
	}
	cfg.scenarioDir = filepath.Join(here, "scenarios")
	cfg.outDir = filepath.Join(here, "results")
	if cfg.claude == "" {
		cfg.claude = findClaude()
	}
	if cfg.stele == "" {
		cfg.stele, err = buildStele(filepath.Join(here, "..", "..", ".."))
		if err != nil {
			fail(err)
		}
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
	fmt.Fprintf(os.Stderr, "%d runs, model %s, claude %s, stele %s\n", len(jobs), cfg.model, cfg.claude, cfg.stele)

	var (
		mu      sync.Mutex
		results []Result
		wg      sync.WaitGroup
		sem     = make(chan struct{}, max(1, *parallel))
	)
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

	sum := summary(results, cfg.model)
	if err := os.WriteFile(filepath.Join(cfg.outDir, stamp+".md"), []byte(sum), 0o644); err != nil {
		fail(err)
	}
	fmt.Print(sum)
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

// buildStele builds stele from the module at root into a temp directory.
func buildStele(root string) (string, error) {
	dir, err := os.MkdirTemp("", "stele-eval-bin-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "stele")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/stele")
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "agent eval:", err)
	os.Exit(2)
}

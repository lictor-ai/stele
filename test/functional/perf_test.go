package functional

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// What stele adds to a session, without a model: the hook's wall time per
// write, and the text it puts in the conversation. The agent eval measures
// the model side (extra round trips, real tokens).

// Hook latency, cold (index built by the hook) and warm, for rule sets of
// increasing size. Claude Code runs the hook twice per write.
func TestMeasureHookLatency(t *testing.T) {
	runs := 40
	if testing.Short() {
		runs = 10
	}
	result := map[string]any{}
	for _, n := range []int{3, 50, 500} {
		p := newProject(t, manyRules(n))

		start := time.Now()
		p.hook(pre("cmd/a.go", "package a")) // builds .stele/index.json
		cold := time.Since(start)

		var preAllow, preDeny, postClean, postViolating []time.Duration
		src := "package a\n\nfunc f() int { return 1 }\n"
		bad := "package a\n\nfunc f() { fmt.Println(1) }\n"
		p.write("cmd/a.go", src)
		p.write("cmd/b.go", bad)
		time.Sleep(3100 * time.Millisecond)
		for i := range runs {
			preAllow = append(preAllow, p.hook(pre("cmd/a.go", src)).Duration)
			e := pre("cmd/a.go", src)
			e.Prompt = fmt.Sprintf("fresh-%d", i)
			preDeny = append(preDeny, p.hook(e).Duration)
			postClean = append(postClean, p.hook(post("cmd/a.go", src)).Duration)
			postViolating = append(postViolating, p.hook(post("cmd/b.go", bad)).Duration)
		}
		result[fmt.Sprintf("%d_rules", n)] = map[string]any{
			"cold_first_write_ms": cold.Milliseconds(),
			"pre_allow":           summarize(preAllow),
			"pre_deny":            summarize(preDeny),
			"post_clean":          summarize(postClean),
			"post_violating":      summarize(postViolating),
		}
		s := summarize(preAllow)
		t.Logf("%d rules: pre allow p50 %.1f ms p95 %.1f ms; cold %v", n, s.P50, s.P95, cold)
		// Claude Code's timeout is 10 s (init sets it); a hook past it lets
		// the write through unchecked. Fail well before.
		if s.P95 > 1000 {
			t.Errorf("%d rules: pre hook p95 %.0f ms", n, s.P95)
		}
	}
	// Process start alone, for scale: stele with no payload.
	p := newProject(t, "")
	var bare []time.Duration
	for range runs {
		bare = append(bare, p.hookRaw(nil).Duration)
	}
	result["process_start_only"] = summarize(bare)
	measure("hook_latency", result)
}

// Recompiling inside the hook after stele.md changes.
func TestMeasureRecompileInHook(t *testing.T) {
	p := newProject(t, manyRules(500))
	p.compile()
	var ds []time.Duration
	for i := range 10 {
		p.write("stele.md", manyRules(500)+fmt.Sprintf("\n---\n\nEdit %d.\n", i))
		e := pre("x.txt", "x")
		e.Prompt = fmt.Sprintf("p%d", i)
		ds = append(ds, p.hook(e).Duration)
	}
	measure("hook_recompile_500_rules", summarize(ds))
}

// Post-write check of large files.
func TestMeasurePostLargeFiles(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	out := map[string]any{}
	for _, size := range []int{10 << 10, 1 << 20, 16 << 20} {
		line := "func f() int { return 1 } // padding padding padding padding\n"
		src := "package a\n" + strings.Repeat(line, size/len(line)) + "func g() { fmt.Println(1) }\n"
		p.write("big.go", src)
		var ds []time.Duration
		for range 5 {
			a := p.hook(post("big.go", "x"))
			ds = append(ds, a.Duration)
			if !a.Block {
				t.Errorf("%d bytes: the violation on the last line was missed", size)
			}
		}
		out[fmt.Sprintf("%d_bytes", size)] = summarize(ds)
	}
	measure("post_large_files", out)
}

// The text the hook puts in front of the model: a deny with the rules once
// per turn, and the violation messages. In tokens, estimated.
func TestMeasureInjectedText(t *testing.T) {
	sets := map[string]string{
		"readme_example_3_rules": goRules,
		"many_unscoped_20_rules": manyUnscoped(20),
	}
	if data, err := os.ReadFile("../../stele.md"); err == nil {
		sets["this_repo_stele_md"] = string(data)
	}
	out := map[string]any{}
	for name, md := range sets {
		p := newProject(t, md)
		a := p.hook(pre("internal/x/x.go", "package x"))
		out[name] = map[string]int{"deny_bytes": len(a.Reason), "deny_est_tokens": tokens(a.Reason), "rules_file_bytes": len(md), "rules_file_est_tokens": tokens(md)}
		t.Logf("%s: deny %d bytes ≈ %d tokens (stele.md ≈ %d tokens)", name, len(a.Reason), tokens(a.Reason), tokens(md))
	}
	p := newProject(t, goRules)
	src := "package a\n\nfunc f() { fmt.Println(1) }\n"
	p.write("a.go", src)
	v := p.hook(post("a.go", src))
	out["one_violation_block"] = map[string]int{"bytes": len(v.Reason), "est_tokens": tokens(v.Reason)}
	measure("injected_text", out)
}

// manyRules makes n rules: a third scoped to Go with a regex, a third scoped
// to other trees, a third guidance for docs, plus one unscoped.
func manyRules(n int) string {
	var parts []string
	for i := range n {
		switch i % 3 {
		case 0:
			parts = append(parts, fmt.Sprintf("scope: [\"**/*.go\"]\ncheck: regex\npattern: fmt\\.Print(ln)?\\(|forbidden_%d\\(\n\nRule %d: never print and never call forbidden_%d.\n", i, i, i))
		case 1:
			parts = append(parts, fmt.Sprintf("scope: [\"svc%d/**\"]\n\nRule %d: service %d owns its schema.\n", i, i, i))
		default:
			parts = append(parts, fmt.Sprintf("scope: docs/\n\nRule %d: docs in plain English.\n", i))
		}
	}
	parts = append(parts, "check: regex\npattern: AKIA[0-9A-Z]{16}\n\nNever put credentials in code.\n")
	return strings.Join(parts, "\n---\n\n")
}

func manyUnscoped(n int) string {
	var parts []string
	for i := range n {
		parts = append(parts, fmt.Sprintf("Unscoped rule %d. Always write clear code, keep functions short, and explain why in comments when the reason is not obvious from the code itself.\n", i))
	}
	return strings.Join(parts, "\n---\n\n")
}

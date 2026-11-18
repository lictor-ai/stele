package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPowerShellTargets(t *testing.T) {
	cwd := t.TempDir()
	at := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }
	type tg struct {
		rel  string
		kind Kind
	}
	tests := []struct {
		cmd     string
		want    []tg
		unknown int
	}{
		{"Set-Content -Path c.txt -Value three", []tg{{"c.txt", Redirect}}, 0},
		{"set-content c.txt three", []tg{{"c.txt", Redirect}}, 0},
		{"sc -Pa 'a b.go' -Val x", []tg{{"a b.go", Redirect}}, 0},
		{"Add-Content -LiteralPath:a[1].go -Value x", []tg{{"a[1].go", Redirect}}, 0},
		{`"x" | Out-File -FilePath out.go -Encoding utf8`, []tg{{"out.go", Redirect}}, 0},
		{"Get-Date | Tee-Object log.txt", []tg{{"log.txt", Redirect}}, 0},
		{"echo x > a.go; echo y >> b.go", []tg{{"a.go", Redirect}, {"b.go", Redirect}}, 0},
		{"go test 2>&1 *> all.log", []tg{{"all.log", Redirect}}, 0},
		{"echo x>a.go", []tg{{"a.go", Redirect}}, 0},
		{"New-Item -ItemType File -Path src/x.go -Value 'package x'", []tg{{"src/x.go", Redirect}}, 0},
		{"New-Item -ItemType Directory src", nil, 0},
		{"ni -Path src -Name x.go", []tg{{"src/x.go", Redirect}}, 0},
		{"Remove-Item a.go, b.go -Force", []tg{{"a.go", Remove}, {"b.go", Remove}}, 0},
		{"del stele.md", []tg{{"stele.md", Remove}}, 0},
		{"Copy-Item src.go -Destination dst.go", []tg{{"dst.go", Copy}}, 0},
		{"Move-Item a.go b.go", []tg{{"a.go", Remove}, {"b.go", Copy}}, 0},
		{"Rename-Item -Path sub/a.go -NewName b.go", []tg{{"sub/a.go", Remove}, {"sub/b.go", Copy}}, 0},
		{"Clear-Content a.go", []tg{{"a.go", Copy}}, 0},
		{"[IO.File]::WriteAllText('a.go', 'x')", []tg{{"a.go", Redirect}}, 0},
		{"[System.IO.File]::Copy('a.go', 'b.go')", []tg{{"b.go", Copy}}, 0},
		{"[IO.File]::Delete(\"c.go\")", []tg{{"c.go", Remove}}, 0},
		{"cd sub; Set-Content a.go x", []tg{{"sub/a.go", Redirect}}, 0},
		{"Set-Location -Path sub && sc a.go x", []tg{{"sub/a.go", Redirect}}, 0},
		{"$f = 'v.go'; Set-Content $f x", []tg{{"v.go", Redirect}}, 0},
		{`$d = "src"; Set-Content "$d/w.go" x`, []tg{{"src/w.go", Redirect}}, 0},
		{"Set-Content $unknown x", nil, 1},
		{"Set-Content $env:TEMP\\x.go x", nil, 1},
		{"Set-Content $(Get-Name) x", nil, 1},
		{"Set-Content -Path env:FOO -Value 1", nil, 0},
		{"echo x > $null", nil, 0},
		{"if ($true) { Set-Content in.go x }", []tg{{"in.go", Redirect}}, 0},
		{"# Set-Content no.go x\nSet-Content yes.go x", []tg{{"yes.go", Redirect}}, 0},
		{"<# block\nSet-Content no.go #> Set-Content yes.go x", []tg{{"yes.go", Redirect}}, 0},
		{"Get-Content a.go; Select-String x b.go; go test ./...", nil, 0},
		{"& Set-Content q.go x", []tg{{"q.go", Redirect}}, 0},
		{"Set-Content `\n -Path cont.go x", []tg{{"cont.go", Redirect}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got, unknown := PowerShellTargets(tt.cmd, cwd)
			var want []Target
			for _, w := range tt.want {
				want = append(want, Target{Path: at(w.rel), Kind: w.kind})
			}
			if len(got) != len(want) || unknown != tt.unknown {
				t.Fatalf("got %v (unknown %d), want %v (unknown %d)", got, unknown, want, tt.unknown)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("target %d = %v, want %v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestPowerShellTargetsAbsoluteAndHome(t *testing.T) {
	cwd := t.TempDir()
	abs := filepath.Join(t.TempDir(), "abs.go")
	got, _ := PowerShellTargets("Set-Content -Path '"+abs+"' x", cwd)
	if len(got) != 1 || got[0].Path != abs {
		t.Errorf("absolute: %v", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got, _ = PowerShellTargets("Set-Content ~/h.go x", cwd)
	if len(got) != 1 || got[0].Path != filepath.Join(home, "h.go") {
		t.Errorf("home: %v", got)
	}
	if got, unknown := PowerShellTargets("Set-Content a.go x", ""); len(got) != 0 || unknown != 1 {
		t.Errorf("no cwd: %v, %d", got, unknown)
	}
}

func TestPowerShellTargetsGlob(t *testing.T) {
	cwd := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "c.md"} {
		if err := os.WriteFile(filepath.Join(cwd, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := PowerShellTargets("Remove-Item *.go", cwd)
	if len(got) != 2 || !strings.HasSuffix(got[0].Path, "a.go") || got[1].Kind != Remove {
		t.Errorf("glob: %v", got)
	}
	got, _ = PowerShellTargets("Remove-Item -LiteralPath *.go", cwd)
	if len(got) != 1 || filepath.Base(got[0].Path) != "*.go" {
		t.Errorf("a literal path is not a glob: %v", got)
	}
}

func TestPowerShellTargetsCaps(t *testing.T) {
	if got, unknown := PowerShellTargets(strings.Repeat("x", maxCommand+1), t.TempDir()); got != nil || unknown != 1 {
		t.Errorf("an over-long command: %v, %d", got, unknown)
	}
	var b strings.Builder
	for i := 0; i < maxTargets+10; i++ {
		b.WriteString("echo x > f" + strings.Repeat("a", i+1) + ".go; ")
	}
	if got, _ := PowerShellTargets(b.String(), t.TempDir()); len(got) != maxTargets {
		t.Errorf("%d targets, want the cap %d", len(got), maxTargets)
	}
	// A variable that holds itself, run four times a level, stops at the
	// budget of nested script read.
	start := time.Now()
	for _, cmd := range []string{
		"$x = 'iex $x; iex $x; iex $x; iex $x'; iex $x",
		// Across dialects: PowerShell, bash -c and eval in one fan-out.
		`$x = 'iex $x; bash -c "eval ''echo > a''; eval x"; pwsh -c "iex x"; iex $x'; iex $x`,
	} {
		r := &results{seen: map[string]int{}}
		p := &psReader{res: r, vars: map[string]string{}, dir: "/w", dirOK: true}
		p.run(cmd)
		if r.unknown == 0 || r.nested <= maxNested {
			t.Errorf("%s: unknown %d, nested %d: the nested budget %d was not hit", cmd, r.unknown, r.nested, maxNested)
		}
		if limit := maxNested + readCost + len(cmd); r.nested > limit {
			t.Errorf("%s: nested %d, past the budget's bound %d", cmd, r.nested, limit)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("iex of itself took %v", d)
	}
}

// A string's variables share the bash budget of bytes expanded (#171): many
// copies of a long variable, or a chain of doublings, stop at it. Counted in
// bytes expanded and allocated, not timed (#138).
func TestPowerShellTargetsExpandBudget(t *testing.T) {
	big := strings.Repeat("a", 50<<10)
	chain := "$v0 = '" + big + "'; "
	for i := range 30 {
		chain += fmt.Sprintf("$v%d = \"$v%d$v%d\"; ", i+1, i, i)
	}
	for _, cmd := range []string{
		"$x = '" + big + "'; Set-Content -Path \"" + strings.Repeat("$x", 20000) + "\" -Value 1",
		"$x = '" + big + "'; echo 1 > \"" + strings.Repeat("${x}", 20000) + "\"",
		chain + "echo 1 > $v30",
	} {
		r := &results{seen: map[string]int{}}
		p := &psReader{res: r, vars: map[string]string{}, dir: "/w", dirOK: true}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		p.run(cmd)
		runtime.ReadMemStats(&after)
		if r.unknown == 0 || r.grown != maxExpand+1 || len(r.out) != 0 {
			t.Errorf("%.40s…: %v, unknown %d, expanded %d: the budget %d was not hit", cmd, r.out, r.unknown, r.grown, maxExpand)
		}
		if got, limit := after.TotalAlloc-before.TotalAlloc, uint64(64<<20); got > limit {
			t.Errorf("%.40s…: allocated %d MiB, limit %d MiB", cmd, got>>20, limit>>20)
		}
	}
	cwd := t.TempDir()
	// Past the budget, a path that expands is unknown; one written out is
	// still seen.
	over := "$x = '" + big + "'; echo 1 > \"" + strings.Repeat("$x", 30) + "\"; echo 1 > b.go"
	if got, unknown := PowerShellTargets(over, cwd); len(got) != 1 || filepath.Base(got[0].Path) != "b.go" || unknown == 0 {
		t.Errorf("past the budget: %v, unknown %d; want b.go and unknown", got, unknown)
	}
	// Within it, a long value named a few times is still read.
	within := "$x = '" + strings.Repeat("a", 100<<10) + "'; Set-Content -Path \"" + strings.Repeat("$x", 9) + ".go\" -Value 1"
	if got, unknown := PowerShellTargets(within, cwd); len(got) != 1 || unknown != 0 {
		t.Errorf("within the budget: %d targets, unknown %d; want 1 and none", len(got), unknown)
	}
}

func FuzzPowerShellTargets(f *testing.F) {
	f.Add("Set-Content -Path a.go -Value x; Remove-Item b.go")
	f.Add("[IO.File]::WriteAllText('a.go', \"$x\")")
	f.Fuzz(func(t *testing.T, cmd string) {
		ts, _ := PowerShellTargets(cmd, "/w")
		for _, x := range ts {
			if x.Path == "" {
				t.Fatalf("empty path from %q", cmd)
			}
		}
	})
}

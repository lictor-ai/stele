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
	env(t, map[string]string{}) // $env:TEMP below is unset
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
		{"New-Item -ItemType Directory src", []tg{{"src", Create}}, 0},
		{"New-Item -Path src -Name .git -ItemType Directory", []tg{{"src/.git", Create}}, 0},
		{"ni -it dir -Path a, b", []tg{{"a", Create}, {"b", Create}}, 0},
		{"New-Item -ItemType File -Path src/stele.json", []tg{{"src/stele.json", Redirect}}, 0},
		{"mkdir build", []tg{{"build", Create}}, 0},
		{"md -Path src/.git -Force", []tg{{"src/.git", Create}}, 0},
		{"mkdir -Path src -Name sub", []tg{{"src/sub", Create}}, 0},
		{"mkdir $d", nil, 1},
		{"git init src", []tg{{"src/.git", Create}}, 0},
		{"git init", []tg{{".git", Create}}, 0},
		{"git -C sub init x", []tg{{"sub/x/.git", Create}}, 0},
		{"git.exe clone https://github.com/o/repo.git", []tg{{"repo/.git", Create}}, 0},
		{"git clone --depth 1 https://h/o/r.git 'src/r'", []tg{{"src/r/.git", Create}}, 0},
		{`git worktree add ..\wt`, []tg{{"../wt/.git", Create}}, 0},
		{"git submodule add https://h/o/lib vendor/lib", []tg{{"vendor/lib/.git", Create}}, 0},
		{"$r = 'repo'; git init $r", []tg{{"repo/.git", Create}}, 0},
		{"git init $unknown", nil, 1},
		{"git --git-dir=x init", nil, 1},
		{"git status; git log --oneline; git diff", nil, 0},
		{"cd sub; git init", []tg{{"sub/.git", Create}}, 0},
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

// $env:NAME, ${env:NAME}, $HOME and $PWD expand, bare and in strings (#177):
// the name's case matters as it does to the OS, and $HOME is the user's
// profile folder on Windows, HOME elsewhere. One unset, empty, or set by the
// command is unknown.
func TestPowerShellTargetsEnv(t *testing.T) {
	cwd := t.TempDir()
	data := filepath.Join(cwd, "appdata")
	env(t, map[string]string{
		"APPDATA": data, "EMPTY": "",
		"HOME": filepath.Join(cwd, "home"), "USERPROFILE": filepath.Join(cwd, "profile"),
	})
	old := windows
	t.Cleanup(func() { windows = old })
	type tg struct {
		rel  string
		kind Kind
	}
	tests := []struct {
		os      string // "" for both, else "windows" or "unix": how names and $HOME are read
		cmd     string
		want    []tg
		unknown int
	}{
		{"", `Remove-Item -Recurse $env:APPDATA\stele`, []tg{{"appdata/stele", Remove}}, 0},
		{"", `Set-Content "$env:APPDATA\x.go" x`, []tg{{"appdata/x.go", Redirect}}, 0},
		{"", `Set-Content ${env:APPDATA}/y.go x`, []tg{{"appdata/y.go", Redirect}}, 0},
		{"", `echo 1 > "${env:APPDATA}\z.go"`, []tg{{"appdata/z.go", Redirect}}, 0},
		{"", `Set-Content $Env:APPDATA/a.go x`, []tg{{"appdata/a.go", Redirect}}, 0},
		{"", `$d = "$env:APPDATA\sub"; Set-Content $d\b.go x`, []tg{{"appdata/sub/b.go", Redirect}}, 0},
		{"", `cd $env:APPDATA; Set-Content c.go x`, []tg{{"appdata/c.go", Redirect}}, 0},
		{"", `git init $env:APPDATA\r`, []tg{{"appdata/r/.git", Create}}, 0},
		{"windows", `Set-Content $env:appdata\w.go x`, []tg{{"appdata/w.go", Redirect}}, 0},
		{"unix", `Set-Content $env:appdata\w.go x`, nil, 1},
		{"windows", `Set-Content $HOME\notes\x.go x`, []tg{{"profile/notes/x.go", Redirect}}, 0},
		{"unix", `Set-Content $HOME\notes\x.go x`, []tg{{"home/notes/x.go", Redirect}}, 0},
		{"windows", `Set-Content "$home/h.go" x; Set-Content ~/t.go x`, []tg{{"profile/h.go", Redirect}, {"profile/t.go", Redirect}}, 0},
		{"unix", `Set-Content "$home/h.go" x; Set-Content ~/t.go x`, []tg{{"home/h.go", Redirect}, {"home/t.go", Redirect}}, 0},
		{"", `cd sub; Set-Content $PWD\p.go x; Set-Content "$pwd/q.go" x`, []tg{{"sub/p.go", Redirect}, {"sub/q.go", Redirect}}, 0},
		{"", `Push-Location sub; Pop-Location; Set-Content $PWD\r.go x`, []tg{{"r.go", Redirect}}, 0},
		{"", `cd $unknown; Set-Content $PWD\x.go x`, nil, 1},
		{"", `Set-Content $env:NOPE\x.go x`, nil, 1},
		{"", `Set-Content $env:EMPTY\x.go x`, nil, 1},
		{"", `Set-Content ${env:}\x.go x`, nil, 1},
		{"", `Set-Content $PSHOME\x.go x`, nil, 1},
		{"", `Set-Content '$env:APPDATA\x.go' x`, nil, 0}, // a drive $env: that is no disk
		{"", "Set-Content \"`$env:APPDATA\\x.go\" x", nil, 0}, // a drive $env: that is no disk
		// Set by the command: unknown from then on, not read from outside.
		{"", `$env:APPDATA = 'elsewhere'; Set-Content $env:APPDATA\x.go x`, nil, 1},
		{"", `${env:APPDATA} = 'elsewhere'; Set-Content $env:APPDATA\x.go x`, nil, 1},
		{"", `Set-Content env:APPDATA x; Set-Content $env:APPDATA\x.go x`, nil, 1},
		{"", `Remove-Item Env:\APPDATA; Set-Content $env:APPDATA\x.go x`, nil, 1},
		{"", `iex '$env:APPDATA = 1'; Set-Content $env:APPDATA\x.go x`, nil, 1},
		{"", `$HOME = 'x'; Set-Content $HOME\x.go x`, nil, 1},
		{"", `$f = 'v.go'; Remove-Item variable:f; Set-Content $f x`, nil, 1},
	}
	for _, tt := range tests {
		for _, win := range []bool{false, true} {
			if (tt.os == "windows" && !win) || (tt.os == "unix" && win) {
				continue
			}
			t.Run(fmt.Sprintf("%s/windows=%v", tt.cmd, win), func(t *testing.T) {
				windows = win
				defer func() { windows = old }()
				got, unknown := PowerShellTargets(tt.cmd, cwd)
				var want []Target
				for _, w := range tt.want {
					want = append(want, Target{Path: filepath.Join(cwd, filepath.FromSlash(w.rel)), Kind: w.kind})
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
	// $env: and $HOME share it, and the environment is read once a name,
	// on Windows whatever case the name is written in: each read there
	// copies the value.
	reads := map[string]int{}
	old, oldWindows := getenv, windows
	t.Cleanup(func() { getenv, windows = old, oldWindows })
	getenv = func(k string) string {
		reads[k]++
		return map[string]string{"HOME": "/h", "USERPROFILE": "/h", "BIG": strings.Repeat("b", 32<<10)}[k]
	}
	spend := "$x = '" + big + "'; echo 1 > \"" + strings.Repeat("$x", 21) + "\"; "
	for _, win := range []bool{false, true} {
		windows = win
		for _, cmd := range []string{
			"echo 1 > \"" + strings.Repeat("$env:BIG", 7000) + "\"",
			"echo 1 > \"" + strings.Repeat("${env:BIG}$Env:BIG", 3500) + "\"",
			spend + "echo 1 > \"" + strings.Repeat("$env:BIG", 7000) + "\"",
			spend + "echo 1 > \"" + strings.Repeat("$HOME", 7000) + "\"",
			spend + strings.Repeat("echo 1 > ~/x; ", 7000),
			"cd '" + big + "'; echo 1 > \"" + strings.Repeat("$PWD", 20000) + "\"",
		} {
			clear(reads)
			r := &results{seen: map[string]int{}}
			p := &psReader{res: r, vars: map[string]string{}, dir: "/w", dirOK: true}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			p.run(cmd)
			runtime.ReadMemStats(&after)
			if r.unknown == 0 || r.grown != maxExpand+1 {
				t.Errorf("windows=%v %.40s…: unknown %d, expanded %d: the budget %d was not hit", win, cmd, r.unknown, r.grown, maxExpand)
			}
			if got, limit := after.TotalAlloc-before.TotalAlloc, uint64(64<<20); got > limit {
				t.Errorf("windows=%v %.40s…: allocated %d MiB, limit %d MiB", win, cmd, got>>20, limit>>20)
			}
			for k, n := range reads {
				if n > 1 {
					t.Errorf("windows=%v %.40s…: %s read %d times", win, cmd, k, n)
				}
			}
		}
	}
	// $env:Big and $env:BIG are one variable on Windows, two elsewhere.
	windows = true
	clear(reads)
	PowerShellTargets(`echo 1 > "$env:big$env:Big$ENV:BIG"`, "/w")
	if len(reads) != 1 || reads["BIG"] != 1 {
		t.Errorf("windows: reads %v, want BIG once", reads)
	}
	windows = false
	clear(reads)
	PowerShellTargets(`echo 1 > "$ENV:BIG$env:Big"`, "/w")
	if len(reads) != 2 || reads["BIG"] != 1 || reads["Big"] != 1 {
		t.Errorf("unix: reads %v, want BIG and Big", reads)
	}
	getenv, windows = old, oldWindows
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
	f.Add("git -C $d clone u; mkdir -Name x; ni -ItemType Directory a, b")
	f.Add(`$env:A = 1; sc "${env:HOME}\$env:A$HOME$PWD" x; Remove-Item Env:\A, variable:x; cd ~; ni ${env:}`)
	f.Fuzz(func(t *testing.T, cmd string) {
		ts, _ := PowerShellTargets(cmd, "/w")
		for _, x := range ts {
			if x.Path == "" {
				t.Fatalf("empty path from %q", cmd)
			}
		}
	})
}

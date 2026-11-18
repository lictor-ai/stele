package shell

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf16"
)

// psFiles runs PowerShellTargets and returns its paths relative to cwd.
func psFiles(t *testing.T, cmd, cwd string) ([]string, int) {
	t.Helper()
	ts, unknown := PowerShellTargets(cmd, cwd)
	var out []string
	for _, x := range ts {
		rel, err := filepath.Rel(cwd, x.Path)
		if err != nil {
			rel = x.Path
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, unknown
}

// encode is s as -EncodedCommand takes it: base64 of UTF-16LE.
func encode(s string) string {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// PowerShell takes \ as a separator on every OS, pwsh on Linux too.
func TestPowerShellBackslashOnEveryOS(t *testing.T) {
	if got, want := psPath(`internal\db.go`), filepath.Join("internal", "db.go"); got != want {
		t.Errorf("psPath = %q, want %q", got, want)
	}
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, _ := psFiles(t, `Copy-Item sub\stele.md dir; Rename-Item -Path sub\a.go -NewName b.go; New-Item -Path sub -Name c.go`, cwd)
	want := []string{"dir/stele.md", "sub/a.go", "sub/b.go", "sub/c.go"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// NUL is a device on Windows only.
func TestPowerShellNULDevice(t *testing.T) {
	old := windows
	windows = true
	t.Cleanup(func() { windows = old })
	if got, unknown := psFiles(t, "echo y > NUL; echo z > nul.txt", t.TempDir()); !slices.Equal(got, []string(nil)) || unknown != 0 {
		t.Errorf("got %v, %d: NUL and nul.txt are devices on Windows", got, unknown)
	}
}

// The forms Codex writes: each names stele.md, which must be seen, or is
// counted as unknown where stele cannot know the path.
func TestPowerShellForms(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, cmd string
		want      []string // seen, in order
		unknown   int      // at least
	}{
		{"here-string with quotes, piped", "@'\nit's \"odd\" ' and '@ inside\n'@ | Set-Content stele.md", []string{"stele.md"}, 0},
		{"expanding here-string", "@\"\nline \"q\"\n\"@ | Out-File stele.json", []string{"stele.json"}, 0},
		{"redirect after a quoted word", "'x'>stele.md", []string{"stele.md"}, 0},
		{"redirect after a double-quoted word", `echo "package db">internal\db.go`, []string{"internal/db.go"}, 0},
		{"redirect after a subexpression", "$(Get-Date)>f.txt", []string{"f.txt"}, 0},
		{"colon and a quoted value", "Set-Content -Path:'stele.md' -Value x", []string{"stele.md"}, 0},
		{"colon, LiteralPath, double quotes", `Add-Content -LiteralPath:"stele.json" -Value x`, []string{"stele.json"}, 0},
		{"assigned output", "$null = New-Item stele.md -Force", []string{"stele.md"}, 0},
		{"assigned copy", "$r = Copy-Item a.txt stele.md", []string{"stele.md"}, 0},
		{"assigned .NET call", "$x = [IO.File]::WriteAllText('stele.md', 'y')", []string{"stele.md"}, 0},
		{"cast", "[void](New-Item stele.md -Force)", []string{"stele.md"}, 0},
		{"parenthesised", "(New-Item stele.md)", []string{"stele.md"}, 0},
		{"common parameters", "Set-Content -ErrorAction SilentlyContinue -ea Stop -OutVariable o stele.md x", []string{"stele.md"}, 0},
		{"-Type", "New-Item -Type File stele.md", []string{"stele.md"}, 0},
		{"an unknown parameter does not shift the path", "Set-Content -Bogus v stele.md -Value x", []string{"stele.md"}, 1},
		{"expression as a path", "Set-Content (Join-Path a b) x", nil, 1},
		{"type method as a path", "Set-Content ([IO.Path]::Combine('a','b')) x", nil, 1},
		{"array literal", "Remove-Item @('a.txt','stele.md')", []string{"a.txt", "stele.md"}, 0},
		{"splatting", "Set-Content @args", nil, 1},
		{"path from the pipeline", "Get-ChildItem *.md | Remove-Item", nil, 1},
		{"popd", "Push-Location dir; Pop-Location; Set-Content stele.md x", []string{"stele.md"}, 0},
		{"copy into a directory", "Copy-Item stele.md dir", []string{"dir/stele.md"}, 0},
		{"move into a directory", "Move-Item a.go -Destination dir", []string{"a.go", "dir/a.go"}, 0},
		{"unspaced assignment", "$f='stele.md'; Set-Content $f x", []string{"stele.md"}, 0},
		{"appended to", "$f = 'a'; $f += '.md'; Set-Content $f x", nil, 1},
		{"set in a block", "$f = 'a.txt'; if ($c) { $f = 'stele.md' }; Set-Content $f x", nil, 1},
		{"module-qualified", `Microsoft.PowerShell.Management\Remove-Item stele.md`, []string{"stele.md"}, 0},
		{"Export-Csv", "Get-Process | Export-Csv -Path stele.json", []string{"stele.json"}, 0},
		{"Start-Transcript", "Start-Transcript stele.md", []string{"stele.md"}, 0},
		{"iwr -OutFile", "iwr https://x -OutFile stele.md", []string{"stele.md"}, 0},
		{"symbolic link", "New-Item -ItemType SymbolicLink -Path stele.md -Target x", []string{"stele.md"}, 0},
		{"async write", "[IO.File]::WriteAllTextAsync('stele.md', 'x')", []string{"stele.md"}, 0},
		{"StreamWriter::new", "$w = [IO.StreamWriter]::new('stele.md')", []string{"stele.md"}, 0},
		{"New-Object StreamWriter", "New-Object IO.StreamWriter 'stele.md'", []string{"stele.md"}, 0},
		{"pwsh -Command", "pwsh -NoProfile -Command 'Set-Content stele.md x'", []string{"stele.md"}, 0},
		{"powershell -c", `powershell.exe -c "Remove-Item stele.json"`, []string{"stele.json"}, 0},
		{"bash -c", "bash -c 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"cmd /c", `cmd /c "echo x > stele.md"`, []string{"stele.md"}, 0},
		{"iex", "iex 'Set-Content stele.md x'", []string{"stele.md"}, 0},
		{"Invoke-Expression -Command", `Invoke-Expression -Command "Remove-Item stele.json"`, []string{"stele.json"}, 0},
		{"iex then cd", "iex 'cd dir'; Set-Content stele.md x", []string{"dir/stele.md"}, 0},
		{"iex of a variable", "iex $cmd", nil, 1},
		{"iex from the pipeline", "'Set-Content stele.md x' | iex", nil, 1},
		{"en dash", "Set-Content \u2013Path stele.md x", []string{"stele.md"}, 0},
		{"em dash", "Remove-Item \u2014LiteralPath stele.md", []string{"stele.md"}, 0},
		{"horizontal bar", "Set-Content \u2015Path stele.md \u2015Value x", []string{"stele.md"}, 0},
		{"en dash on pwsh", "pwsh \u2013NoProfile \u2013Command 'Set-Content stele.md x'", []string{"stele.md"}, 0},
		{"-EncodedCommand", "pwsh -EncodedCommand " + encode("Set-Content stele.md x"), []string{"stele.md"}, 0},
		{"-enc", "powershell -enc " + encode("Remove-Item stele.json"), []string{"stele.json"}, 0},
		{"-EncodedCommand not base64", "pwsh -EncodedCommand !!", nil, 1},
		{"-EncodedCommand with a byte order mark", "pwsh -EncodedCommand " + encode("\uFEFFSet-Content stele.md x"), []string{"stele.md"}, 0},
		{"bash -cx", "bash -cx 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"wsl with options", "wsl -d Ubuntu -u root --exec bash -lc 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"wsl, no -e", "wsl.exe sh -c 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"wsl running no shell", "wsl ls", nil, 1},
		{"bash script, -c its argument", "bash script.sh -c 'echo x > stele.md'", nil, 1},
		{"bash -o takes -c", "bash -o -c 'echo x > stele.md'", nil, 1},
		{"bash -lc", "bash -lc 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"sh -e -c", "sh -e -c 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"zsh -c", "zsh -c 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"wsl -e sh -c", "wsl -e sh -c 'echo x > stele.md'", []string{"stele.md"}, 0},
		{"bash -c that does not parse", `bash -c 'echo "x > stele.md'`, nil, 1},
		{"Start-Process", "Start-Process pwsh -ArgumentList '-c','x'", nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unknown := psFiles(t, tt.cmd, cwd)
			if !slices.Equal(got, tt.want) || unknown < tt.unknown {
				t.Errorf("%s: got %v (unknown %d), want %v (unknown ≥ %d)", tt.cmd, got, unknown, tt.want, tt.unknown)
			}
		})
	}
}

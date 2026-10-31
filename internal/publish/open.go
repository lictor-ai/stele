package publish

import (
	"errors"
	"os"
	"os/exec"
	"runtime"

	"github.com/lictor-ai/stele/internal/toolpath"
)

// openCommand is the program that opens a link in the browser on goos, and
// the arguments that go before the link.
func openCommand(goos string) (name string, prefix []string) {
	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler"}
	case "darwin":
		return "open", nil
	}
	return "xdg-open", nil
}

// Open opens link in the browser, never through a shell: the link is one
// argument of the opener, found on PATH and never inside the project at
// root. It returns once the opener has started. The caller ignores a
// failure: the link is printed anyway.
func Open(root, link string) error {
	if !sameHostHTTPS(link, hostOf(link)) {
		return errors.New("not an https link")
	}
	name, prefix := openCommand(runtime.GOOS)
	bin, err := resolveTool(name, root)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, append(prefix, link)...)
	cmd.Env = toolpath.Env(os.Environ(), root)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

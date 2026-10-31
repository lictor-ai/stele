package safefile

import (
	"os"
	"path/filepath"
	"strings"
)

// maxHops bounds the links Resolve follows, against a loop.
const maxHops = 32

// Resolve returns where a write to file lands, following each link on the
// way, a dangling final one included, which a write would create. It follows
// Windows junctions too, which filepath.EvalSymlinks does not. ok is false
// when it cannot tell.
func Resolve(file string) (string, bool) {
	p := filepath.Clean(file)
	for hops := 0; ; {
		vol := filepath.VolumeName(p)
		parts := strings.Split(strings.TrimPrefix(p[len(vol):], string(filepath.Separator)), string(filepath.Separator))
		cur := vol + string(filepath.Separator)
		next := ""
		for i, part := range parts {
			step := filepath.Join(cur, part)
			info, err := os.Lstat(step)
			if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
				cur = step // a plain directory, or one the write would create
				continue
			}
			to, err := os.Readlink(step)
			if err != nil {
				cur = step // a reparse point that is not a link
				continue
			}
			if !filepath.IsAbs(to) {
				to = filepath.Join(cur, to)
			}
			next = filepath.Join(append([]string{to}, parts[i+1:]...)...)
			break
		}
		if next == "" {
			return cur, true
		}
		if hops++; hops > maxHops {
			return "", false
		}
		p = filepath.Clean(next)
	}
}

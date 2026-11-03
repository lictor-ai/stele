//go:build unix

package safefile

import "syscall"

// noFollow makes opening a symlink fail, so Append never writes through one.
const noFollow = syscall.O_NOFOLLOW

//go:build !unix

package safefile

// noFollow is zero where opening cannot refuse a link: Append checks what it
// opened instead.
const noFollow = 0

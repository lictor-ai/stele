package main

import "syscall"

// shortPath is p's 8.3 short form (C:\Users\NACER~1), or "".
func shortPath(p string) string {
	return convertPath(p, syscall.GetShortPathName)
}

// longPath is p's long form, or "".
func longPath(p string) string {
	return convertPath(p, syscall.GetLongPathName)
}

func convertPath(p string, conv func(in *uint16, out *uint16, n uint32) (uint32, error)) string {
	in, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	n, err := conv(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

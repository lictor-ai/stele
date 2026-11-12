//go:build !windows

package main

// shortPath and longPath exist only on Windows.
func shortPath(string) string { return "" }

func longPath(string) string { return "" }

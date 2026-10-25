// Package report prints reports.
package report

import "fmt"

// Header prints the report header.
func Header(title string) {
	fmt.Printf("== %s ==\n", title)
}

// Line prints one report line.
func Line(name string, n int) {
	fmt.Printf("%-20s %d\n", name, n)
}

// Footer prints the report footer.
func Footer() {
	fmt.Printf("== end ==\n")
}

// Command app is the operator CLI.
package main

import (
	"fmt"
	"os"

	"example.com/app/internal/cmds"
)

func main() {
	if len(os.Args) < 2 {
		for _, c := range cmds.All {
			fmt.Printf("  %-10s %s\n", c.Name, c.Usage)
		}
		os.Exit(2)
	}
	for _, c := range cmds.All {
		if c.Name == os.Args[1] {
			if err := c.Run(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
	os.Exit(2)
}

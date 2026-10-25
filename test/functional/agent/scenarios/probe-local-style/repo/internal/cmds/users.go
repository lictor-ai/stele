package cmds

import "fmt"

// Users prints the users report.
func Users(args []string) error {
	fmt.Printf("%-10s %s\n", "users:", "alice, bob, carol")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

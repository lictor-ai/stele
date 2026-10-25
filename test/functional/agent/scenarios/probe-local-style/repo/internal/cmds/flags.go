package cmds

import "fmt"

// Flags prints the flags report.
func Flags(args []string) error {
	fmt.Printf("%-10s %s\n", "flags:", "new-ui=on beta-api=off")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

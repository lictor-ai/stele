package cmds

import "fmt"

// Queues prints the queues report.
func Queues(args []string) error {
	fmt.Printf("%-10s %s\n", "queues:", "email=4 billing=0")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

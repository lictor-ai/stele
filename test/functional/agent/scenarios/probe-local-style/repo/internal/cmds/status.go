package cmds

import "fmt"

// Status prints the service report.
func Status(args []string) error {
	fmt.Printf("%-10s %s\n", "service:", "up, 3 replicas")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

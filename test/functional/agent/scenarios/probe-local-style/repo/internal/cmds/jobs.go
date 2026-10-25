package cmds

import "fmt"

// Jobs prints the jobs report.
func Jobs(args []string) error {
	fmt.Printf("%-10s %s\n", "jobs:", "reindex (running), cleanup (idle)")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

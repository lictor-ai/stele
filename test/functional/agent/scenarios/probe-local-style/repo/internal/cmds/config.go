package cmds

import "fmt"

// Config prints the config report.
func Config(args []string) error {
	fmt.Printf("%-10s %s\n", "config:", "env=dev region=eu-west-1")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

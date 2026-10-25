package cmds

import "fmt"

// Tenants prints the tenants report.
func Tenants(args []string) error {
	fmt.Printf("%-10s %s\n", "tenants:", "acme, globex")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

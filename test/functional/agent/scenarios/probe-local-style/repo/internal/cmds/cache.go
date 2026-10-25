package cmds

import "fmt"

// Cache prints the cache report.
func Cache(args []string) error {
	fmt.Printf("%-10s %s\n", "cache:", "hits=912 misses=88")
	if len(args) > 0 {
		fmt.Printf("(ignoring %d arguments)\n", len(args))
	}
	return nil
}

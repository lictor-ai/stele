// Package cmds holds the app's subcommands.
package cmds

// Command is one subcommand.
type Command struct {
	Name  string
	Usage string
	Run   func(args []string) error
}

// All lists the subcommands, in the order help shows them.
var All = []Command{
	{"status", "show the service status", Status},
	{"users", "list users", Users},
	{"config", "print the effective config", Config},
	{"jobs", "list background jobs", Jobs},
	{"queues", "show queue depths", Queues},
	{"cache", "show cache statistics", Cache},
	{"tenants", "list tenants", Tenants},
	{"flags", "list feature flags", Flags},
}

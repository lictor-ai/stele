// Package adapter is the agent adapter slot: the Adapter interface, and the
// agent-neutral types the hook flow decides with (ADR-0009). Everything that
// knows one agent's shape is in its own package below this one, and only
// that package and the registry in cmd/stele import it.
package adapter

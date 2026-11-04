// Package stats says how the rules are doing, from the log alone (D70): the
// rules broken most and whether the agent fixed them right after, the rules
// no write met, and the writes denied per day, for all agents and for each.
// It reads .stele/log.jsonl through journal.Read and nothing else; below
// MinTurns it judges nothing (O-LOG-VOLUME).
package stats

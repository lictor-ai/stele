// Package journal keeps .stele/log.jsonl, the local record of what stele
// does (ADR-0010): the rules a session loaded, the writes it denied and the
// rules they showed, the checks after a write and what they found, and each
// stele check run. It is what stele stats reads.
//
// A line holds rule ids, paths and counts, never code. It stays on the
// machine: only a sink listed in stele.json sends it anywhere (D53).
//
// Each line is signed on its own with this machine's key (D57), so a log a
// cloned repository committed, or a line edited by hand, is skipped on read.
// Lines can still be deleted; that only loses data.
package journal

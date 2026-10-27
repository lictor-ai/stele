package install

import "github.com/lictor-ai/stele/internal/adapter/claudecode"

// cc is the adapter the install tests check stele's real output with. This
// is the one file outside cmd/stele/agents.go that may name an adapter
// (ADR-0009).
var cc = claudecode.Adapter{}

// ccWords are its words, as the hook hands them to the messages.
var ccWords = cc.Words()

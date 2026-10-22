package plugin

import (
	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/check/regex"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// Builtins is the built-in sources: always present, no stele.json entry
// needed. It is all EnsureFresh ever gets (D45).
func Builtins() []source.Source {
	return []source.Source{stelemd.New()}
}

// BuiltinCheckers is the built-in checkers, by name. In v1 they are the only
// checkers (ADR-0004); external ones from stele.json join them with #14.
func BuiltinCheckers() map[string]check.Checker {
	return map[string]check.Checker{regex.Name: regex.New()}
}

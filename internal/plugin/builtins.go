package plugin

import (
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// Builtins is the built-in sources: always present, no stele.json entry
// needed. It is all EnsureFresh ever gets (D45).
func Builtins() []source.Source {
	return []source.Source{stelemd.New()}
}

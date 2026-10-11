// Package source is the Source slot: Rule and the Source interface.
//
// Only a source opens rule files (D40), and only to read them into rules or to
// say whether they changed. There is no write path (D38).
package source

import (
	"context"
	"fmt"
)

// Rule is one rule as a source produced it. Identity is not part of it: ids
// are assigned by internal/lock, except for a source that owns its ids (D52).
type Rule struct {
	Description string            // optional; absent means the matcher keys on Body
	Tags        []string          // free vocabulary
	Scope       []string          // path globs
	Body        string            // the rule text
	Meta        map[string]string // every other key, kept as written (D46)
	ID          string            // optional; set only by a source that owns its ids (D52)
	Source      string            // the source's name, e.g. "stelemd"
	File        string            // relative to the root, for error messages
	Position    int               // ordinal within this source, for reconciliation
	Line        int               // 1-based line the rule starts on, for error messages
}

// Source turns rule files into rules.
type Source interface {
	// Name identifies the source in the lock and the index.
	Name() string
	// Read returns the source's rules and the fingerprint of the files they
	// came from.
	Read(ctx context.Context, root string) (rules []Rule, fingerprint string, err error)
	// Fingerprint says whether the source changed since the last Read. It must
	// be cheap.
	Fingerprint(ctx context.Context, root string) (string, error)
}

// ParseError is bad input in a rule file. It names the file and the line.
type ParseError struct {
	File string // relative to the root
	Line int    // 1-based
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d: %v", e.File, e.Line, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

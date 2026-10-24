package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/session"
)

// maxProblemsShown caps the invalid rules one warning names; the rest are
// counted.
const maxProblemsShown = 5

// invalidRules says which rules the post-write check cannot use (#54): once
// a session per set of problems to the user (warning), and to the agent
// (context) when the write it answers was to stele.md itself, so the agent
// knows whether the rule it wrote works.
//
// It checks the rules again rather than reading problems from the index:
// that is one regexp compile per checker rule, which the post-write check
// already pays. Only the built-in checkers run (D45). Once checkers come from
// stele.json (#14), a rule naming one would show as "not a checker" here:
// leave those out then.
func invalidRules(ev claudecode.Event) (warning, context string) {
	root, rel, ok := target(ev)
	if !ok {
		return "", ""
	}
	key, err := loadKey()
	if err != nil {
		return "", ""
	}
	// afterWrite has just refreshed the index; after a parse error this is
	// the last good one, which is what is enforced.
	idx, err := index.Load(root, key)
	if err != nil {
		return "", ""
	}
	ps := check.Validate(idx, plugin.BuiltinCheckers())
	if len(ps) == 0 {
		return "", ""
	}
	msg := invalidRulesMessage(idx, ps)
	if rel == "stele.md" {
		context = msg
	}

	mark := rulesMark(ps)
	if st, err := session.Load(root, ev.SessionID, key); err == nil && st.RulesWarned == mark {
		return "", context
	}
	warn := true
	_, err = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		warn = st.RulesWarned != mark
		st.RulesWarned = mark
		return nil
	})
	if err != nil || warn {
		return msg, context // a warning too many beats none
	}
	return "", context
}

// rulesMark names a set of problems: a hash of the sorted rule id and
// message pairs.
func rulesMark(ps []check.Problem) string {
	pairs := make([]string, len(ps))
	for i, p := range ps {
		pairs[i] = p.RuleID + "\x00" + p.Message
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\x00\x00")))
	return hex.EncodeToString(sum[:8])
}

// invalidRulesMessage lists up to maxProblemsShown problems with the rule's
// id, excerpt and place in its source, read from the index so the hot path
// reads no lock.
func invalidRulesMessage(idx index.Index, ps []check.Problem) string {
	where := map[string]string{}
	for _, e := range idx.Rules {
		where[e.ID] = fmt.Sprintf(" (%s rule %d)", e.Source, e.Position+1)
	}
	line := func(p check.Problem) string {
		return fmt.Sprintf("%s \"%s\"%s: %s", p.RuleID, p.Excerpt, where[p.RuleID], p.Message)
	}
	const tail = "The other rules still apply; run stele check to see every problem."
	if len(ps) == 1 {
		return fmt.Sprintf("stele: 1 rule cannot be checked and is skipped: %s. %s", line(ps[0]), tail)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "stele: %d rules cannot be checked and are skipped:", len(ps))
	for i, p := range ps {
		if i == maxProblemsShown {
			fmt.Fprintf(&b, "\n- and %d more", len(ps)-i)
			break
		}
		b.WriteString("\n- " + line(p))
	}
	b.WriteString("\n" + tail)
	return b.String()
}

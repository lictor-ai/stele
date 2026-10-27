package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/session"
)

// maxProblemsShown caps the invalid rules one warning names; the rest are
// counted.
const maxProblemsShown = 5

// invalidRules says which rules the post-write check cannot use (#54): once
// a session per set of problems to the user (warning), and to the agent
// (context) when the write it answers was to stele.md itself, so the agent
// knows whether the rule it wrote works. The context fits in room bytes, what
// the post-write check's own context leaves of install.MaxMessage.
//
// It checks the rules again rather than reading problems from the index:
// that is one regexp compile per checker rule, which the post-write check
// already pays. Only the built-in checkers run (D45). Once checkers come from
// stele.json (#14), a rule naming one would show as "not a checker" here:
// leave those out then.
func invalidRules(ev adapter.Event, room int) (warning, context string) {
	root, rel, ok := target(ev.File)
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
	msg := invalidRulesMessage(idx, ps, install.MaxMessage)
	if rel == "stele.md" {
		context = msg
		if len(msg) > room {
			context = invalidRulesMessage(idx, ps, room)
		}
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

// maxProblemMessage bounds one problem's message, in characters, as the
// violation messages are bounded.
const maxProblemMessage = 200

// invalidRulesMessage lists up to maxProblemsShown problems with the rule's
// id, excerpt and place in its source, read from the index so the hot path
// reads no lock. It fits in limit bytes: whole entries are left out, counted
// in "and N more", and nothing is said when not even the count fits.
func invalidRulesMessage(idx index.Index, ps []check.Problem, limit int) string {
	where := map[string]string{}
	for _, e := range idx.Rules {
		where[e.ID] = fmt.Sprintf(" (%s rule %d)", e.Source, e.Position+1)
	}
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = fmt.Sprintf("%s \"%s\"%s: %s", p.RuleID, p.Excerpt, where[p.RuleID], cut(p.Message, maxProblemMessage))
	}
	const tail = "The other rules still apply; run stele check to see every problem."
	render := func(shown int) string {
		if len(ps) == 1 && shown == 1 {
			return fmt.Sprintf("stele: 1 rule cannot be checked and is skipped: %s. %s", lines[0], tail)
		}
		var b strings.Builder
		if len(ps) == 1 {
			b.WriteString("stele: 1 rule cannot be checked and is skipped:")
		} else {
			fmt.Fprintf(&b, "stele: %d rules cannot be checked and are skipped:", len(ps))
		}
		for _, l := range lines[:shown] {
			b.WriteString("\n- " + l)
		}
		if shown < len(ps) {
			fmt.Fprintf(&b, "\n- and %d more", len(ps)-shown)
		}
		b.WriteString("\n" + tail)
		return b.String()
	}
	for shown := min(len(ps), maxProblemsShown); shown >= 0; shown-- {
		if msg := render(shown); len(msg) <= limit {
			return msg
		}
	}
	return ""
}

// cut shortens s to n characters, marking the cut with "…".
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

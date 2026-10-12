package lock

import (
	"fmt"

	"github.com/lictor-ai/stele/internal/source"
)

// Report says how Reconcile matched each rule to an id.
type Report struct {
	Supplied, ByHash, ByPosition, Minted, Dropped int
	// Guessed holds the ids matched by position. They are inferences: a wrong
	// one moves a rule's history to another rule, so they are printed.
	Guessed []string
	// Rekeyed holds the rules whose id changed because their source started
	// supplying one.
	Rekeyed []Rekey
}

// Rekey is a lock id replaced by the id a source supplied.
type Rekey struct{ Old, New string }

// Reconcile gives each rule an id, matching it against prev. ids is parallel
// to rules, and next is the lock to save. It is pure: newID mints UUIDs.
//
// A rule is matched in this order (spec §3):
//
//  0. its source supplied an id → that id (D52)
//  1. a lock entry has the same hash → that entry's id
//  2. a lock entry has the same source and position → that entry's id, guessed
//  3. otherwise → a new id
//
// Each lock entry is used at most once. Two rules supplying the same id is
// ErrDuplicateID. The project id is minted when prev has none and never
// changed after.
func Reconcile(rules []source.Rule, prev File, newID func() string) (ids []string, next File, rep Report, err error) {
	hashes := make([]string, len(rules))
	for i, r := range rules {
		hashes[i] = source.Hash(r.Body)
	}

	used := make([]bool, len(prev.Rules))
	claim := func(match func(Entry) bool) (string, bool) {
		for j, e := range prev.Rules {
			if !used[j] && match(e) {
				used[j] = true
				return e.ID, true
			}
		}
		return "", false
	}

	ids = make([]string, len(rules))
	owner := make(map[string]int) // supplied id → rule index

	// 0. Supplied ids. The lock entry that held this id, or else the one that
	// held this body, is spent, so no other rule inherits it.
	for i, r := range rules {
		if r.ID == "" {
			continue
		}
		if j, ok := owner[r.ID]; ok {
			return nil, File{}, Report{}, fmt.Errorf("%w %s: supplied by %s and %s",
				ErrDuplicateID, r.ID, where(rules[j]), where(r))
		}
		owner[r.ID] = i
		ids[i] = r.ID
		rep.Supplied++
	}
	held := make(map[string]bool) // supplied ids the lock already had
	for id := range owner {
		_, held[id] = claim(func(e Entry) bool { return e.ID == id })
	}
	for i, r := range rules {
		if r.ID == "" || held[r.ID] {
			continue
		}
		// Same source and position first, as in pass 1, so two rules with
		// one body keep their own ids.
		sameBody := func(e Entry) bool {
			_, supplied := owner[e.ID]
			return !supplied && e.Hash == hashes[i]
		}
		old, ok := claim(func(e Entry) bool {
			return sameBody(e) && e.Source == r.Source && e.Position == r.Position
		})
		if !ok {
			old, ok = claim(sameBody)
		}
		if ok {
			rep.Rekeyed = append(rep.Rekeyed, Rekey{Old: old, New: r.ID})
		}
	}

	// 1. Same hash. An entry at the same source and position goes first, so
	// two rules with one body keep their own ids.
	for i, r := range rules {
		if ids[i] == "" {
			if id, ok := claim(func(e Entry) bool {
				return e.Hash == hashes[i] && e.Source == r.Source && e.Position == r.Position
			}); ok {
				ids[i] = id
				rep.ByHash++
			}
		}
	}
	for i := range rules {
		if ids[i] == "" {
			if id, ok := claim(func(e Entry) bool { return e.Hash == hashes[i] }); ok {
				ids[i] = id
				rep.ByHash++
			}
		}
	}

	// 2. Same source and position: a reworded rule.
	for i, r := range rules {
		if ids[i] == "" {
			if id, ok := claim(func(e Entry) bool { return e.Source == r.Source && e.Position == r.Position }); ok {
				ids[i] = id
				rep.ByPosition++
				rep.Guessed = append(rep.Guessed, id)
			}
		}
	}

	// 3. A new rule.
	for i := range rules {
		if ids[i] == "" {
			ids[i] = newID()
			rep.Minted++
		}
	}

	for _, u := range used {
		if !u {
			rep.Dropped++
		}
	}

	next = File{Version: version, Project: prev.Project, Rules: make([]Entry, len(rules))}
	if next.Project == "" {
		next.Project = newID()
	}
	for i, r := range rules {
		next.Rules[i] = Entry{ID: ids[i], Source: r.Source, Hash: hashes[i], Position: r.Position}
	}
	return ids, next, rep, nil
}

// where names a rule for an error message.
func where(r source.Rule) string {
	if r.File != "" && r.Line > 0 {
		return fmt.Sprintf("%s:%d", r.File, r.Line)
	}
	return fmt.Sprintf("%s position %d", r.Source, r.Position)
}

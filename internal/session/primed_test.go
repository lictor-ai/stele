package session

import (
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPrimedRoundTrip(t *testing.T) {
	root := t.TempDir()
	primed := map[string]map[string]string{"": {"sql": "h1"}, "sub": {"log": "h2"}}
	if _, err := Update(root, "s1", t0, testKey, func(s *State) error {
		s.Primed = primed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(primed, got.Primed); diff != "" {
		t.Errorf("primed (-want +got):\n%s", diff)
	}
}

// A state file saved before Primed existed still loads, with nothing primed.
func TestLoadStateWithoutPrimed(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	old := `{"session_id": "s1", "prompt_id": "p1", "shown": {"": {"sql": "2026-10-12T09:00:00Z"}}}` + "\n"
	if err := os.WriteFile(path(root, "s1"), testKey.Seal(purpose("s1"), []byte(old)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Primed != nil {
		t.Errorf("old state primed = %v, want none", got.Primed)
	}
}

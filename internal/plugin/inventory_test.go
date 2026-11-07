package plugin

import (
	"errors"
	"testing"
)

func TestInventoryLists(t *testing.T) {
	root := steleJSON(t, `{"sources": [
		{"name": "git", "config": {"repo": "git@github.com:acme/g.git"}},
		{"name": "docs"}
	], "checkers": [{"name": "lint"}]}`)
	items, err := Inventory(root, noGit{t}, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, string(it.Kind)+" "+it.Name)
		if it.Err != nil {
			t.Errorf("%s %s: %v", it.Kind, it.Name, it.Err)
		}
	}
	want := []string{"source stelemd", "source git:github.com/acme/g/stele.md", "source docs", "checker regex", "checker lint"}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
	if !items[0].Builtin || !items[1].Builtin || items[2].Builtin || !items[3].Builtin || items[4].Builtin {
		t.Errorf("builtin flags wrong: %+v", items)
	}
	if items[2].Source == nil {
		t.Error("the found external source has no Source")
	}
}

func TestInventoryOfflineStartsNothing(t *testing.T) {
	root := steleJSON(t, `{"sources": [{"name": "docs"}], "checkers": [{"name": "lint"}]}`)
	items, err := Inventory(root, noGit{t}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Err != nil {
			t.Errorf("%s %s: %v", it.Kind, it.Name, it.Err)
		}
		if it.Kind == KindSource && it.Name == "docs" && it.Source != nil {
			t.Error("an external source was built without a runner")
		}
	}
}

func TestInventoryMissingPluginIsAnItem(t *testing.T) {
	root := steleJSON(t, `{"checkers": [{"name": "lint"}, {"name": "Bad"}]}`)
	items, err := Inventory(root, noGit{t}, &fakeRunner{findErr: ErrPluginNotFound})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 { // stelemd, regex, lint, Bad
		t.Fatalf("items = %+v", items)
	}
	if !errors.Is(items[2].Err, ErrPluginNotFound) {
		t.Errorf("lint err = %v, want not found", items[2].Err)
	}
	if items[3].Err == nil {
		t.Error("a bad name is not reported")
	}
}

func TestInventoryBadSteleJSON(t *testing.T) {
	root := steleJSON(t, `{"sources": [`)
	if _, err := Inventory(root, noGit{t}, nil); err == nil {
		t.Error("a stele.json that does not parse is not an error")
	}
}

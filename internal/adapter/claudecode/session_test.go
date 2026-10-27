package claudecode

import (
	"path/filepath"
	"strconv"
	"testing"
)

// A session hook's source and a subagent's type decide whether stele may
// count the rules file as loaded (#68).
func TestLoaded(t *testing.T) {
	root := t.TempDir()
	cwd := strconv.Quote(root)
	tests := []struct {
		name, payload string
		root          string
		agent         string
		launch, ok    bool
	}{
		{"startup launches", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":` + cwd + `,"source":"startup"}`, root, "", true, true},
		{"resume launches", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":` + cwd + `,"source":"resume"}`, root, "", true, true},
		{"compact reloads", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":` + cwd + `,"source":"compact"}`, root, "", false, true},
		{"clear reloads", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":` + cwd + `,"source":"clear"}`, root, "", false, true},
		{"general-purpose loads", `{"hook_event_name":"SubagentStart","session_id":"s1","cwd":` + cwd + `,"agent_id":"a1","agent_type":"general-purpose"}`, root, "a1", false, true},
		{"Explore does not", `{"hook_event_name":"SubagentStart","session_id":"s1","cwd":` + cwd + `,"agent_id":"a1","agent_type":"Explore"}`, root, "", false, false},
		{"no agent id", `{"hook_event_name":"SubagentStart","session_id":"s1","cwd":` + cwd + `,"agent_type":"general-purpose"}`, root, "", false, false},
		{"not in the root", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":` + cwd + `,"source":"startup"}`, filepath.Dir(root), "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := Parse([]byte(tt.payload))
			if err != nil {
				t.Fatal(err)
			}
			agent, launch, ok := Loaded(ev, tt.root)
			if agent != tt.agent || launch != tt.launch || ok != tt.ok {
				t.Errorf("Loaded = %q, %v, %v; want %q, %v, %v", agent, launch, ok, tt.agent, tt.launch, tt.ok)
			}
		})
	}
}

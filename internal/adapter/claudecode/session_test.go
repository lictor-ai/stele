package claudecode

import "testing"

// A session hook's source and a subagent's type decide whether stele may
// count the rules file as loaded (#68).
func TestParseSessionFields(t *testing.T) {
	e, err := Parse([]byte(`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/p","source":"compact"}`))
	if err != nil || e.Source != "compact" {
		t.Errorf("SessionStart: %+v, %v; want source compact", e, err)
	}
	e, err = Parse([]byte(`{"hook_event_name":"SubagentStart","session_id":"s1","cwd":"/p","agent_id":"a1","agent_type":"Explore"}`))
	if err != nil || e.AgentID != "a1" || e.AgentType != "Explore" {
		t.Errorf("SubagentStart: %+v, %v; want agent a1 of type Explore", e, err)
	}
}

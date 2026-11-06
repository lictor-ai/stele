package codex

import (
	"regexp"
	"strconv"
	"strings"
)

// exitLine is the first line of apply_patch's result: "Exit code: 0".
var exitLine = regexp.MustCompile(`(?m)^Exit code: (-?\d+)\r?$`)

// changedHeader opens the list of files apply_patch changed.
const changedHeader = "Success. Updated the following files:"

// patchResult reads what apply_patch said it did, from a PostToolUse's
// tool_response, measured on Codex 0.166.0:
//
//	Exit code: 0
//	Wall time: 0.7 seconds
//	Output:
//	Success. Updated the following files:
//	A b.txt
//	M other.txt
//	D deleteme.txt
//
// failed says the patch did not apply. changed maps each path it lists, as
// written, to its letter: A added, M updated, D deleted. ok is false when the
// result is not this shape: then the patch alone says what was written.
func patchResult(resp string) (changed map[string]byte, failed, ok bool) {
	m := exitLine.FindStringSubmatch(resp)
	if m == nil {
		return nil, false, false
	}
	if code, err := strconv.Atoi(m[1]); err != nil || code != 0 {
		return nil, true, true
	}
	_, list, found := strings.Cut(resp, changedHeader)
	if !found {
		return nil, false, false
	}
	changed = map[string]byte{}
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 3 || line[1] != ' ' || !strings.ContainsRune("AMD", rune(line[0])) {
			continue
		}
		changed[line[2:]] = line[0]
	}
	return changed, false, true
}

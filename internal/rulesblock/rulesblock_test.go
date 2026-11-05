package rulesblock

import "testing"

// The hash is what a session saved by an older stele holds: it must never
// change, or a session loaded by one binary reads as unloaded by the next.
func TestHashIsPinned(t *testing.T) {
	got := Read([]byte("intro\n<!-- stele rule r1 -->\nscope: **/*.go\nUse the logger.\n"))
	const want = "6c6768d84f7ba4bc5d140ce3a624bef6"
	if got["r1"] != want {
		t.Fatalf("hash = %q, want %q", got["r1"], want)
	}
}

func TestBlocksIgnoreLineEndsAndBlankLines(t *testing.T) {
	a := Read([]byte("<!-- stele rule r1 -->\nbody\n\n<!-- stele rule r2 -->\nother\n"))
	b := Read([]byte("\xef\xbb\xbf<!-- stele rule r1 -->  \r\n\r\nbody  \r\n<!-- stele rule r2 -->\r\nother"))
	if a["r1"] != b["r1"] || a["r2"] != b["r2"] || len(a) != 2 {
		t.Fatalf("a = %v, b = %v", a, b)
	}
}

func TestDuplicateIDDropped(t *testing.T) {
	got := Read([]byte("<!-- stele rule r1 -->\na\n<!-- stele rule r1 -->\nb\n"))
	if len(got) != 0 {
		t.Fatalf("got %v, want nothing", got)
	}
}

func TestValidID(t *testing.T) {
	if !ValidID("0b2c-ab:1.x_y") || ValidID("a b") || ValidID("") || ValidID("x-->") {
		t.Fatal("ValidID wrong")
	}
}

func TestSame(t *testing.T) {
	if !Same([]byte("x\n<!-- stele rule r -->\nb\n"), []byte("y\n<!-- stele rule r -->\nb")) {
		t.Fatal("intro text should not count")
	}
	if Same([]byte("<!-- stele rule r -->\nb\n"), []byte("<!-- stele rule r -->\nc\n")) {
		t.Fatal("body change should count")
	}
}

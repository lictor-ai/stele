package jsonedit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoundTripKeepsValuesAndLayout(t *testing.T) {
	in := "\xef\xbb\xbf{\r\n  \"a\": 1.50,\r\n  \"b\\u0041\": \"<x>\"\r\n}\r\n"
	d, empty := Split([]byte(in))
	if empty || !d.BOM || !d.CRLF {
		t.Fatalf("Split = %+v, %v", d, empty)
	}
	o, err := ParseObject(d.Body, "the file")
	if err != nil {
		t.Fatal(err)
	}
	c, err := o.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("got %q, want %q", out, in)
	}
}

func TestSetRemoveGet(t *testing.T) {
	o, _ := ParseObject([]byte(`{"a":1}`), "x")
	o.Set("b", []byte(`[1]`))
	o.Set("a", []byte(`2`))
	o.Remove("missing")
	if v, _ := o.Get("a"); string(v) != "2" {
		t.Fatalf("a = %s", v)
	}
	c, _ := o.Marshal()
	if string(c) != `{"a":2,"b":[1]}` {
		t.Fatalf("got %s", c)
	}
	o.Remove("a")
	if _, ok := o.Get("a"); ok {
		t.Fatal("a still there")
	}
}

func TestParseObjectRefusesDuplicateKey(t *testing.T) {
	if _, err := ParseObject([]byte(`{"a":1,"a":2}`), "x"); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidNamesPosition(t *testing.T) {
	d, _ := Split([]byte("{\n  \"a\": ,\n}"))
	if err := d.Valid(); err == nil || !strings.Contains(err.Error(), "line 2, column 8:") {
		t.Fatalf("err = %v", err)
	}
	// The column is the byte in error's, here the brace after a trailing
	// comma, at the start of its line.
	d, _ = Split([]byte("{\n  \"a\": 1,\n}\n"))
	if err := d.Valid(); err == nil || !strings.Contains(err.Error(), "line 3, column 1:") {
		t.Fatalf("err = %v", err)
	}
	d, _ = Split([]byte(`{"a": 1,}`))
	if err := d.Valid(); err == nil || !strings.Contains(err.Error(), "line 1, column 9:") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range []struct{ in, want string }{
		// A column counts characters, not bytes.
		{"{\n  \"a\": \"éé\",}", "line 2, column 13:"},
		// A carriage return ends the line before; it moves no column.
		{"{\r\n  \"a\": 1,\r\n}\r\n", "line 3, column 1:"},
		// At the end of the input, the place after the last character.
		{`{"a": 1`, "line 1, column 8:"},
	} {
		d, _ = Split([]byte(c.in))
		if err := d.Valid(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Valid(%q) = %v, want %q", c.in, err, c.want)
		}
	}
}

func TestMarshalKeepsHTML(t *testing.T) {
	b, _ := Marshal("a<b>&c")
	if string(b) != `"a<b>&c"` {
		t.Fatalf("got %s", b)
	}
	if got := string(Array([]json.RawMessage{[]byte("1"), []byte(`"<x>"`)})); got != `[1,"<x>"]` {
		t.Fatalf("Array = %s", got)
	}
}

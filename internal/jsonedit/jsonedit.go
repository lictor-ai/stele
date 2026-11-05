// Package jsonedit edits a JSON settings file stele shares with the user and
// their agent: it changes the values stele owns and writes every other value
// back byte for byte, so number literals, escapes and key order survive, and
// keeps the file's byte order mark and line ends.
package jsonedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Member is one key of a JSON object, its key and value kept exactly as
// written. Raw is empty for a key stele adds.
type Member struct {
	Key   string
	Value json.RawMessage
	Raw   []byte
}

// Object is a JSON object in its written order.
type Object []Member

// Get returns key's value.
func (o Object) Get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Set replaces key's value, or appends key when it is missing.
func (o *Object) Set(key string, v json.RawMessage) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = v
			return
		}
	}
	*o = append(*o, Member{Key: key, Value: v})
}

// Remove removes key.
func (o *Object) Remove(key string) {
	for i := range *o {
		if (*o)[i].Key == key {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

// Marshal writes the object compact, each key as written.
func (o Object) Marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k := m.Raw
		if k == nil {
			var err error
			if k, err = Marshal(m.Key); err != nil {
				return nil, err
			}
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.Value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Marshal encodes v without escaping <, > and &, as the agents write them.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Array joins values already encoded into a JSON array. json.Marshal would
// re-escape them.
func Array(vs []json.RawMessage) json.RawMessage {
	b := []byte{'['}
	for i, v := range vs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, v...)
	}
	return append(b, ']')
}

// ParseObject reads a JSON object. what names it in errors. A key that
// appears twice is an error: which one the agent keeps is not stele's to
// guess.
func ParseObject(data []byte, what string) (Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	var o Object
	seen := map[string]bool{}
	for dec.More() {
		start := dec.InputOffset()
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := t.(string) // an object's tokens alternate key, value
		// The key's bytes as written, escapes and all, without the comma
		// and space before it.
		raw := bytes.TrimLeft(data[start:dec.InputOffset()], ", \t\r\n")
		if seen[key] {
			return nil, fmt.Errorf("%q appears twice in %s", key, what)
		}
		seen[key] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, Member{Key: key, Value: v, Raw: raw})
	}
	return o, nil
}

var bom = []byte("\xef\xbb\xbf")

// Doc is a settings file's body and how it was written.
type Doc struct {
	Body      []byte // without the byte order mark
	BOM, CRLF bool
}

// Split reads how data is written. Empty says there is no JSON at all.
func Split(data []byte) (d Doc, empty bool) {
	body, hasBOM := bytes.CutPrefix(data, bom)
	return Doc{Body: body, BOM: hasBOM, CRLF: bytes.Contains(body, []byte("\r\n"))}, len(bytes.TrimSpace(body)) == 0
}

// Valid checks that the body is JSON, naming the line and column of a syntax
// error.
func (d Doc) Valid() error {
	var v any
	if err := json.Unmarshal(d.Body, &v); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			line, col := position(d.Body, se.Offset)
			return fmt.Errorf("invalid JSON at line %d, column %d: %v", line, col, err)
		}
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}

// Render writes compact JSON back laid out with two spaces, as the agents
// do, with the doc's byte order mark and line ends.
func (d Doc) Render(compact []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, compact, "", "  "); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	out := b.Bytes()
	if d.CRLF {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	if d.BOM {
		out = append(append([]byte{}, bom...), out...)
	}
	return out, nil
}

// position turns a byte offset into a 1-based line and column.
func position(data []byte, offset int64) (line, col int) {
	before := data[:min(int(offset), len(data))]
	line = bytes.Count(before, []byte("\n")) + 1
	col = len(before) - bytes.LastIndexByte(before, '\n')
	return line, col
}

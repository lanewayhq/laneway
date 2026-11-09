package fileblock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A JSON document is decoded into these values, so that rewriting it keeps
// its keys in their order and its numbers as written:
//
//	*object, []any, string, json.Number, bool, nil
//
// Formatting (indentation aside) is not kept, which is why files are only
// rewritten when their content changes.

// object is a JSON object that keeps its keys in order.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// set replaces key's value in place, or adds key at the end.
func (o *object) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// parseObject decodes data, which must hold one JSON object.
func parseObject(data []byte) (*object, error) {
	v, err := parseValue(data)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*object)
	if !ok {
		return nil, errors.New("the top level is not an object")
	}
	return o, nil
}

// parseValue decodes data, which must hold one JSON value.
func parseValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the top-level value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := newObject()
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			// A repeated key keeps its first position and its last value,
			// as most JSON readers do.
			o.set(key.(string), v)
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		list := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := dec.Token()
		return list, err
	}
	return tok, nil
}

// encodeObject writes o as indented JSON, with a final newline.
func encodeObject(o *object, indent string) []byte {
	var b bytes.Buffer
	encodeValue(&b, o, indent, "")
	b.WriteByte('\n')
	return b.Bytes()
}

func encodeValue(b *bytes.Buffer, v any, indent, prefix string) {
	inner := prefix + indent
	switch v := v.(type) {
	case *object:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range v.keys {
			b.WriteString(inner)
			encodeString(b, k)
			b.WriteString(": ")
			encodeValue(b, v.vals[k], indent, inner)
			if i < len(v.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(prefix + "}")
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range v {
			b.WriteString(inner)
			encodeValue(b, item, indent, inner)
			if i < len(v)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(prefix + "]")
	case string:
		encodeString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case bool:
		fmt.Fprint(b, v)
	case nil:
		b.WriteString("null")
	default:
		panic(fmt.Sprintf("fileblock: can't encode %T", v))
	}
}

func encodeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	b.Truncate(b.Len() - 1) // Encode's newline
}

// indentOf returns the indentation data uses: the leading whitespace of its
// first indented line, else two spaces.
func indentOf(data []byte) string {
	for _, line := range strings.Split(string(data), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// equalJSON reports whether a and b encode the same.
func equalJSON(a, b any) bool {
	var ea, eb bytes.Buffer
	encodeValue(&ea, a, "", "")
	encodeValue(&eb, b, "", "")
	return bytes.Equal(ea.Bytes(), eb.Bytes())
}

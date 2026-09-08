package rtkreducer

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestScanObjectStringsPreservesExactSpans(t *testing.T) {
	raw := []byte(" { \"huge\" : 1.2300e+4000, \"stdout\" : \"a\\nb\", \"nested\": {\"x\":true} } \n")
	found, ok := scanObjectStrings(raw, map[string]struct{}{"stdout": {}}, 8, 32)
	if !ok {
		t.Fatal("valid object rejected")
	}
	span := found["stdout"]
	if span.value != "a\nb" || string(raw[span.start:span.end]) != `"a\nb"` {
		t.Fatalf("span = %#v token=%q", span, raw[span.start:span.end])
	}
	replacement, _ := json.Marshal("short")
	patched := append([]byte(nil), raw[:span.start]...)
	patched = append(patched, replacement...)
	patched = append(patched, raw[span.end:]...)
	if !bytes.Equal(bytes.Replace(raw, []byte(`"a\nb"`), replacement, 1), patched) {
		t.Fatalf("unselected bytes changed: %q", patched)
	}
}

func TestScanObjectStringsRejectsUnsafeDocuments(t *testing.T) {
	tests := map[string][]byte{
		"scalar root":      []byte(`"value"`),
		"trailing":         []byte(`{} true`),
		"duplicate root":   []byte(`{"x":1,"\u0078":2}`),
		"duplicate nested": []byte(`{"outer":{"x":1,"x":2}}`),
		"wrong selected":   []byte(`{"stdout":123}`),
		"invalid utf8":     append([]byte(`{"x":"`), 0xff, '"', '}'),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, ok := scanObjectStrings(raw, map[string]struct{}{"stdout": {}}, 8, 32); ok {
				t.Fatal("unsafe document accepted")
			}
		})
	}
}

func TestScanObjectStringsBoundsDepthAndNodes(t *testing.T) {
	if _, ok := scanObjectStrings([]byte(`{"a":{"b":1}}`), nil, 2, 32); ok {
		t.Fatal("over-depth document accepted")
	}
	if _, ok := scanObjectStrings([]byte(`{"a":1,"b":2}`), nil, 8, 4); ok {
		t.Fatal("over-node document accepted")
	}
	if _, ok := scanObjectStrings([]byte(`{"a":1}`), nil, 8, 3); !ok {
		t.Fatal("document at node bound rejected")
	}
}

func FuzzJSONMirror(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"stdout":"hello","n":1}`),
		[]byte(`{"\u0078":"a","nested":[true,null,{"z":"q"}]}`),
		[]byte(`{"x":1,"x":2}`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		found, ok := scanObjectStrings(raw, map[string]struct{}{"stdout": {}}, 32, 1024)
		if !ok {
			return
		}
		if !json.Valid(raw) {
			t.Fatal("scanner accepted invalid JSON")
		}
		for _, span := range found {
			if span.start < 0 || span.end > len(raw) || span.start >= span.end || raw[span.start] != '"' || raw[span.end-1] != '"' {
				t.Fatalf("invalid span %#v for %q", span, raw)
			}
		}
	})
}

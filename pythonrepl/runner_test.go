package pythonrepl

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestProtocolRejectsMalformedFrames(t *testing.T) {
	frame := func(size uint32, body string) []byte {
		result := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(result[:4], size)
		copy(result[4:], body)
		return result
	}
	tests := map[string][]byte{
		"zero":          frame(0, ""),
		"over maximum":  frame(33, "{}"),
		"truncated":     frame(8, "{}"),
		"invalid JSON":  frame(1, "{"),
		"unknown field": frame(13, `{"value":1,"x":2}`),
		"trailing JSON": frame(4, `{}{} `),
		"duplicate key": frame(21, `{"value":1,"value":2}`),
		"invalid UTF-8": frame(3, string([]byte{'"', 0xff, '"'})),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			var destination struct {
				Value int `json:"value"`
			}
			if err := readFrame(bytes.NewReader(raw), 32, &destination); err == nil {
				t.Fatalf("frame accepted: %x", raw)
			}
		})
	}
}

func TestProtocolEncodingIsLengthPrefixedAndBounded(t *testing.T) {
	encoded, err := encodeFrame(map[string]int{"value": 42}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if int(binary.BigEndian.Uint32(encoded[:4])) != len(encoded)-4 {
		t.Fatalf("header=%d body=%d", binary.BigEndian.Uint32(encoded[:4]), len(encoded)-4)
	}
	if _, err := encodeFrame(map[string]string{"value": "too large"}, 2); err == nil {
		t.Fatal("oversized frame encoded")
	}
}

package backgroundjobs

import (
	"strings"
	"sync"
	"unicode/utf8"
)

type tailWriter struct {
	mu        sync.Mutex
	buffer    []byte
	start     int
	length    int
	truncated bool
}

func newTailWriter(limit int) *tailWriter {
	return &tailWriter{buffer: make([]byte, limit)}
}

func (writer *tailWriter) Write(data []byte) (int, error) {
	written := len(data)
	writer.mu.Lock()
	defer writer.mu.Unlock()
	capacity := len(writer.buffer)
	if len(data) >= capacity {
		if len(data) > capacity || writer.length > 0 {
			writer.truncated = true
		}
		copy(writer.buffer, data[len(data)-capacity:])
		writer.start = 0
		writer.length = capacity
		return written, nil
	}
	if writer.length+len(data) > capacity {
		discard := writer.length + len(data) - capacity
		writer.start = (writer.start + discard) % capacity
		writer.length -= discard
		writer.truncated = true
	}
	end := (writer.start + writer.length) % capacity
	first := min(len(data), capacity-end)
	copy(writer.buffer[end:], data[:first])
	copy(writer.buffer, data[first:])
	writer.length += len(data)
	return written, nil
}

func (writer *tailWriter) markTruncated() {
	writer.mu.Lock()
	writer.truncated = true
	writer.mu.Unlock()
}

func (writer *tailWriter) snapshot() TailResult {
	writer.mu.Lock()
	data := make([]byte, writer.length)
	if writer.length > 0 {
		first := min(writer.length, len(writer.buffer)-writer.start)
		copy(data, writer.buffer[writer.start:writer.start+first])
		copy(data[first:], writer.buffer[:writer.length-first])
	}
	truncated := writer.truncated
	writer.mu.Unlock()
	if !utf8.Valid(data) {
		data = []byte(strings.ToValidUTF8(string(data), "�"))
	}
	return TailResult{Text: string(data), Truncated: truncated}
}

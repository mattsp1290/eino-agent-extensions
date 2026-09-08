package rtkreducer

import (
	"bytes"
	"io"
)

// boundedCapture consumes a stream without retaining more than limit bytes.
// Once the limit is crossed it keeps draining the stream (the caller normally
// cancels the child at that point) so a writer cannot remain blocked on a full
// pipe. The overflow bit is deliberately separate from the retained bytes:
// truncated output is never a successful result.
type boundedCapture struct {
	limit    int
	retain   bool
	buf      bytes.Buffer
	count    int
	overflow bool
}

func newBoundedCapture(limit int) *boundedCapture {
	return &boundedCapture{limit: limit, retain: true}
}

func newDiscardCapture(limit int) *boundedCapture {
	return &boundedCapture{limit: limit}
}

func (c *boundedCapture) readFrom(r io.Reader, overflow func()) error {
	if c == nil || r == nil || c.limit <= 0 {
		return io.ErrShortBuffer
	}
	chunk := make([]byte, 32<<10)
	var readErr error
	zeroReads := 0
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			zeroReads = 0
			if _, writeErr := c.write(chunk[:n], overflow); writeErr != nil {
				return writeErr
			}
		} else if err == nil {
			zeroReads++
			if zeroReads >= 100 {
				return io.ErrNoProgress
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}
	if readErr == io.EOF {
		return nil
	}
	return readErr
}

// write implements the bounded half of an os/exec-managed stream copy. It
// always reports the full input consumed so the pipe continues draining after
// overflow while retaining at most limit bytes.
func (c *boundedCapture) write(value []byte, overflow func()) (int, error) {
	if c == nil || c.limit <= 0 {
		return 0, io.ErrShortBuffer
	}
	remaining := c.limit - c.count
	keep := len(value)
	if keep > remaining {
		keep = remaining
	}
	if keep < 0 {
		keep = 0
	}
	if c.retain && keep > 0 {
		if _, err := c.buf.Write(value[:keep]); err != nil {
			return 0, err
		}
	}
	c.count += keep
	if keep != len(value) && !c.overflow {
		c.overflow = true
		if overflow != nil {
			overflow()
		}
	}
	return len(value), nil
}

type captureWriter struct {
	capture  *boundedCapture
	overflow func()
}

func (w captureWriter) Write(value []byte) (int, error) {
	return w.capture.write(value, w.overflow)
}

func (c *boundedCapture) bytes() []byte {
	if c == nil {
		return nil
	}
	return c.buf.Bytes()
}

package rtkreducer

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBoundedCaptureRetainsOnlyCompletePrefix(t *testing.T) {
	capture := newBoundedCapture(4)
	var stopped bool
	err := capture.readFrom(strings.NewReader("abcde"), func() { stopped = true })
	if err != nil || !capture.overflow || !stopped {
		t.Fatalf("capture err=%v overflow=%v stopped=%v", err, capture.overflow, stopped)
	}
	if got := string(capture.bytes()); got != "abcd" {
		// The retained prefix is bounded, but overflow still makes the
		// complete transport unsuccessful.
		t.Fatalf("retained overflow bytes %q", got)
	}
}

func TestBoundedCaptureDiscardStillCountsOverflow(t *testing.T) {
	capture := newDiscardCapture(3)
	if err := capture.readFrom(strings.NewReader("abcd"), nil); err != nil {
		t.Fatal(err)
	}
	if !capture.overflow || len(capture.bytes()) != 0 {
		t.Fatalf("discard capture = overflow %v bytes %d", capture.overflow, len(capture.bytes()))
	}
}

func TestBoundedCaptureReturnsReaderError(t *testing.T) {
	want := errors.New("fixture")
	capture := newBoundedCapture(8)
	err := capture.readFrom(errorReader{err: want}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want %v", err, want)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

var _ io.Reader = errorReader{}

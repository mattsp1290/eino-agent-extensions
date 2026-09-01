package backgroundjobs

import (
	"sync"
	"testing"
)

func TestTailWriterRetainsExactSuffix(t *testing.T) {
	writer := newTailWriter(5)
	if n, err := writer.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatalf("first write = %d, %v", n, err)
	}
	_, _ = writer.Write([]byte("cdef"))
	result := writer.snapshot()
	if result.Text != "bcdef" || !result.Truncated {
		t.Fatalf("snapshot = %#v", result)
	}
	_, _ = writer.Write([]byte("123456789"))
	result = writer.snapshot()
	if result.Text != "56789" || !result.Truncated {
		t.Fatalf("oversize snapshot = %#v", result)
	}
}

func TestTailWriterSanitizesInvalidUTF8AndSupportsConcurrentSnapshots(t *testing.T) {
	writer := newTailWriter(32)
	_, _ = writer.Write([]byte{0xff, 'a'})
	if result := writer.snapshot(); result.Text != "�a" {
		t.Fatalf("invalid UTF-8 snapshot = %q", result.Text)
	}
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for count := 0; count < 100; count++ {
				_, _ = writer.Write([]byte("x"))
				_ = writer.snapshot()
			}
		}()
	}
	group.Wait()
}

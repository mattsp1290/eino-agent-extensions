package commandguard

import (
	"io"
	"runtime"
	"strings"
	"testing"
)

type stackSampleReader struct {
	reader io.Reader
	peak   uint64
}

func (r *stackSampleReader) Read(p []byte) (int, error) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.StackInuse > r.peak {
		r.peak = stats.StackInuse
	}
	// Sample while the parser's recursive construction stack is still active.
	if len(p) > 32 {
		p = p[:32]
	}
	return r.reader.Read(p)
}
func TestParserConstructionEnvelope(t *testing.T) {
	for name, s := range map[string]string{"parentheses": strings.Repeat("(", 2000) + "echo ok" + strings.Repeat(")", 2000), "substitutions": strings.Repeat("echo $(", 500) + "echo ok" + strings.Repeat(")", 500)} {
		t.Run(name, func(t *testing.T) {
			if len(s) > testOptions().Limits.MaxCommandBytes {
				t.Fatal("measurement exceeds example bound")
			}
			runtime.GC()
			type sample struct {
				allocated, stack uint64
				err              error
			}
			done := make(chan sample, 1)
			go func() {
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				reader := &stackSampleReader{reader: strings.NewReader(s), peak: before.StackInuse}
				_, err := parseScript(reader, DialectPOSIX)
				runtime.ReadMemStats(&after)
				done <- sample{after.TotalAlloc - before.TotalAlloc, reader.peak - before.StackInuse, err}
			}()
			result := <-done
			if result.err != nil {
				t.Fatal(result.err)
			}
			t.Logf("%d input bytes: %d allocated bytes; %d bytes peak sampled process stack growth during construction", len(s), result.allocated, result.stack)
			// A release check on these measured fixtures, not a universal memory bound.
			if result.allocated > 4<<20 || result.stack > 8<<20 {
				t.Fatal("example parser construction envelope requires re-audit")
			}
		})
	}
}

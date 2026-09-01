package backgroundjobs

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestManagerOpaqueIDAndSupervisorStatusContracts(t *testing.T) {
	valid := "job_0123456789abcdef0123456789abcdef_0123456789abcdef"
	if !validJobID(valid) {
		t.Fatalf("valid ID rejected: %q", valid)
	}
	for _, invalid := range []string{"", "job_short", "job_0123456789ABCDEF0123456789abcdef_0123456789abcdef", "job_0123456789abcdef0123456789abcdef-0123456789abcdef"} {
		if validJobID(invalid) {
			t.Fatalf("invalid ID accepted: %q", invalid)
		}
	}
	for raw, want := range map[string]int{"v1:0\n": 0, "v1:255\n": 255} {
		valid, code := parseSupervisorStatus([]byte(raw), nil)
		if !valid || code != want {
			t.Fatalf("status %q = %t,%d", raw, valid, code)
		}
	}
	for _, raw := range []string{"", "v1:00\n", "v1:256\n", "v1:0\nv1:1\n", "secret:0\n"} {
		if valid, _ := parseSupervisorStatus([]byte(raw), nil); valid {
			t.Fatalf("malformed status accepted: %q", raw)
		}
	}
}

func TestWaitSupervisorReadyClosesReaderOnFailure(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitSupervisorReady(reader, time.Second); err == nil {
		t.Fatal("empty readiness response accepted")
	}
	if err := reader.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("readiness reader remained open: %v", err)
	}
}

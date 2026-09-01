//go:build !linux && !darwin

package backgroundjobs

import "testing"

func TestUnsupportedProcessBackendRejectsExplicitly(t *testing.T) {
	if platformSupported() {
		t.Fatal("unsupported build reported platform support")
	}
	if _, err := prepareProcess(canonicalOptions{}, ".", "echo", newTailWriter(1), newTailWriter(1)); err == nil || err.Error() != "background jobs configuration invalid: code=unsupported-platform" {
		t.Fatalf("unsupported backend error = %v", err)
	}
}

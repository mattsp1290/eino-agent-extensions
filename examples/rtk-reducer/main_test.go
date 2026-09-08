package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExampleRunsSyntheticReducerJourney(t *testing.T) {
	rtkPath := os.Getenv("RTK_REDUCER_TEST_EXECUTABLE")
	if rtkPath == "" {
		t.Skip("RTK_REDUCER_TEST_EXECUTABLE is not set")
	}
	if !filepath.IsAbs(rtkPath) {
		t.Fatal("RTK_REDUCER_TEST_EXECUTABLE must be absolute")
	}
	versionContext, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	versionCommand := exec.CommandContext(versionContext, rtkPath, "--version")
	versionCommand.Env = []string{"HOME=" + t.TempDir(), "RTK_TELEMETRY_DISABLED=1", "NO_COLOR=1", "TERM=dumb", "LANG=C", "LC_ALL=C"}
	version, err := versionCommand.Output()
	if err != nil || strings.TrimSpace(string(version)) != "rtk 0.48.0" {
		t.Fatalf("RTK version = %q, err=%v; want rtk 0.48.0", version, err)
	}
	contents, err := os.ReadFile(rtkPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runExample(ctx, rtkPath, hex.EncodeToString(digest[:]), &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "reduced=true") || !strings.Contains(text, "[Reduced by RTK go-test") {
		t.Fatalf("output=%q; expected a marked reduction", text)
	}
	if !strings.Contains(text, "original_stdout_bytes=") || !strings.Contains(text, "original_result_bytes=") || !strings.Contains(text, "reduced_result_bytes=") {
		t.Fatalf("output=%q; missing bounded byte counts", text)
	}
}

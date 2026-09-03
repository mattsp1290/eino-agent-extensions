//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleRunsStateClearJourney(t *testing.T) {
	python := os.Getenv("PYTHON_REPL_TEST_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Fatal("PYTHON_REPL_TEST_PYTHON is required")
		}
		python, err = filepath.Abs(python)
		if err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := runExample(context.Background(), python, &output); err != nil {
		t.Fatal(err)
	}
	want := "assigned=completed result=42 cleared=true generation=1 after_clear=python_error"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("output=%q want %q", output.String(), want)
	}
}

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRenderSyntheticInstructions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	section, err := renderSyntheticInstructions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outer := strings.Index(section, `path="../AGENTS.md"`)
	root := strings.Index(section, `path="./AGENTS.md"`)
	if outer < 0 || root <= outer || !strings.Contains(section, "outer synthetic policy") || !strings.Contains(section, "project synthetic policy") {
		t.Fatalf("section = %q", section)
	}
}

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSearchSyntheticSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := searchSyntheticSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Title != "Synthetic source" || result.Results[0].URL != "https://example.test/source" || result.Results[0].Snippet != "Deterministic source record." {
		t.Fatalf("result=%#v", result)
	}
	if strings.Contains(result.Results[0].URL, "secret") {
		t.Fatalf("credential URL retained: %#v", result)
	}
}

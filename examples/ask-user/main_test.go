package main

import (
	"context"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/askuser"
)

func TestAskSyntheticQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := askSyntheticQuestion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != askuser.StatusSelected || result.Answer != "Stable" || result.SelectedOption != 2 {
		t.Fatalf("result = %#v", result)
	}
}

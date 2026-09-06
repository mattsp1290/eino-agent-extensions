package main

import (
	"context"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/delegatetask"
)

func TestDelegateSyntheticTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := delegateSyntheticTask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != delegatetask.StatusCompleted || result.Output != "synthetic inspection complete" {
		t.Fatalf("result=%#v", result)
	}
}

func TestDeterministicRunnerRejectsUnknownProfile(t *testing.T) {
	response, err := deterministicRunner(context.Background(), delegatetask.Request{Profile: "unknown"})
	if err != nil || response.Status != delegatetask.ResponseRejected {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

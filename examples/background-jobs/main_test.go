//go:build linux || darwin

package main

import (
	"context"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/backgroundjobs"
)

func TestExampleStartPollAndTimeout(t *testing.T) {
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := runBackgroundJob(ctx, workspace, `printf 'example-ready'`, nil)
	if err != nil || status.State != backgroundjobs.JobSucceeded || status.Stdout.Text != "example-ready" {
		t.Fatalf("success = %#v, %v", status, err)
	}
	one := int64(1)
	status, err = runBackgroundJob(ctx, workspace, `sleep 3`, &one)
	if err != nil || status.State != backgroundjobs.JobTimedOut {
		t.Fatalf("timeout = %#v, %v", status, err)
	}
}

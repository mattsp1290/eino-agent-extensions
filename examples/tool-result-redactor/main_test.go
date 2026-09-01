package main

import (
	"context"
	"testing"
)

func TestExampleMountAndCleanup(t *testing.T) {
	mount, err := mountRedactor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deactivateAndClose(mount)
}

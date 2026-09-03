//go:build !linux && !darwin

package pythonrepl

import (
	"context"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
)

func TestUnsupportedPlatformRejectsBeforePathWork(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	component := extension.Component{InstanceID: "python-repl", Artifact: extension.Artifact{
		Name: "python-repl", Version: "test", Hash: "artifact", SourceKind: extension.SourceNative,
	}}
	_, err = Mount(context.Background(), registry, component, Options{})
	if err == nil || !strings.Contains(err.Error(), "unsupported-platform") {
		t.Fatalf("unsupported mount error = %v", err)
	}
}

// Command tool-result-redactor demonstrates credential-free native mounting.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
)

func main() {
	mount, err := mountRedactor(context.Background())
	if err != nil {
		panic(err)
	}
	deactivateAndClose(mount)
	fmt.Println("tool-result redactor mounted and closed")
}

func mountRedactor(ctx context.Context) (*composition.Mount, error) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return nil, err
	}
	component := extension.Component{
		InstanceID: "example-tool-result-redactor",
		Artifact: extension.Artifact{
			Name: "tool-result-redactor", Version: "example", Hash: "host-supplied-artifact-hash",
			SourceKind: extension.SourceNative,
		},
	}
	return toolresultredactor.Mount(ctx, registry, component, toolresultredactor.Options{
		ExcludedTools: []string{"host-owned-safe-tool"},
		AdditionalPatterns: []toolresultredactor.Pattern{{
			ID: "example-synthetic-marker", Expression: `EXAMPLE_MARKER_[A-Z]+`,
		}},
		Limits: toolresultredactor.Limits{
			MaxFieldBytes: 64 << 10, MaxStructuredBytes: 256 << 10,
			MaxStructuredDepth: 32, MaxStructuredNodes: 10_000,
			MaxAttachments: 32, MaxMetadataEntries: 128,
			MaxMatchesPerField: 256, MaxPatterns: 32, MaxPatternBytes: 4 << 10,
		},
	})
}

func deactivateAndClose(mount *composition.Mount) {
	mount.Deactivate()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mount.Close(ctx); err != nil {
		panic(err)
	}
}

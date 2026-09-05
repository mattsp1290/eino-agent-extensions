package websearch

import (
	"context"
	"encoding/json"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mattsp1290/eino-agent/tools"
)

func definition(options canonicalOptions, coordinator *coordinator) tools.Definition {
	reflector := jsonschema.Reflector{
		Anonymous: true, DoNotReference: true, AllowAdditionalProperties: false,
		RequiredFromJSONSchemaTags: true,
	}
	return tools.Definition{
		Name:        ToolName,
		Description: "Search one query and return bounded title, URL, and snippet source records.",
		Parameters:  einoschema.NewParamsOneOfByJSONSchema(reflector.Reflect(toolInput{})),
		Normalize:   normalizeInput(options),
		Pattern: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if _, err := decodeCanonicalInput(raw); err != nil {
				return "", err
			}
			return permissionPattern, nil
		},
		Execute: func(ctx context.Context, execution tools.Execution) (json.RawMessage, error) {
			input, err := decodeCanonicalInput(execution.Input)
			if err != nil {
				return nil, err
			}
			return coordinator.search(ctx, execution.Call, execution.Context, input)
		},
		RetrySafe: false, Permissions: []string{PermissionSearch}, Retention: options.retention,
		Metadata: map[string]string{"package": "websearch-v1"},
	}
}

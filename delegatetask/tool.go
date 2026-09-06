package delegatetask

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
		Name: ToolName,
		Description: "Delegate one bounded task under an opaque host profile. " +
			"Returns completed, failed, rejected, unavailable, or timed_out.",
		Parameters: einoschema.NewParamsOneOfByJSONSchema(reflector.Reflect(toolInput{})),
		Normalize:  normalizeInput(options),
		Pattern: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			input, err := decodeCanonicalInput(raw)
			if err != nil {
				return "", err
			}
			return "delegate-profile:" + input.Profile, nil
		},
		Execute: func(ctx context.Context, execution tools.Execution) (json.RawMessage, error) {
			input, err := decodeCanonicalInput(execution.Input)
			if err != nil {
				return nil, err
			}
			result, err := coordinator.run(ctx, execution.Call, execution.Context, input)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return nil, runtimeError("result-encoding")
			}
			return encoded, nil
		},
		RetrySafe: false, Permissions: []string{PermissionDelegate}, Retention: options.retention,
		Metadata: map[string]string{"package": "delegatetask-v1"},
	}
}

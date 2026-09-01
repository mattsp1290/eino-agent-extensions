//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestToolSchemasForbidAdditionalPropertiesAndResultsHaveStableJSON(t *testing.T) {
	canonical, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	for _, definition := range definitions(canonical, manager) {
		tool, err := tools.Materialize(context.Background(), definition, runtime.ToolScopeContext{})
		if err != nil {
			t.Fatal(err)
		}
		schema, err := tool.Info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(schema)
		if !strings.Contains(string(raw), `"additionalProperties":false`) {
			t.Fatalf("%s schema permits extra properties: %s", definition.Name, raw)
		}
	}
	start := definitions(canonical, manager)[0]
	tool, _ := tools.Materialize(context.Background(), start, runtime.ToolScopeContext{})
	for _, raw := range []string{
		`{"command":"x","timeout_seconds":1.5}`,
		`{"command":"x","timeout_seconds":9223372036854775808}`,
		`{"command":"x","working_directory":"/tmp"}`,
		`{"command":"x","working_directory":"../escape"}`,
	} {
		if _, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(raw)); !errors.Is(err, tools.ErrMalformedInput) {
			t.Fatalf("input %s error = %v", raw, err)
		}
	}
	if _, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"x","timeout_seconds":5}`)); err != nil {
		t.Fatalf("maximum timeout boundary rejected: %v", err)
	}

	exit := 7
	status := StatusResult{ID: "job_0123456789abcdef0123456789abcdef_0000000000000001", State: JobFailed, StartedAt: "2026-08-31T12:00:00Z", CompletedAt: "2026-08-31T12:00:01Z", TimeoutSeconds: 1, ExitCode: &exit, Stdout: TailResult{Text: "out", Truncated: true}, Stderr: TailResult{Text: "err"}}
	raw, _ := json.Marshal(status)
	want := `{"id":"job_0123456789abcdef0123456789abcdef_0000000000000001","state":"failed","started_at":"2026-08-31T12:00:00Z","completed_at":"2026-08-31T12:00:01Z","timeout_seconds":1,"exit_code":7,"stdout":{"text":"out","truncated":true},"stderr":{"text":"err","truncated":false}}`
	if string(raw) != want {
		t.Fatalf("status JSON = %s", raw)
	}
}

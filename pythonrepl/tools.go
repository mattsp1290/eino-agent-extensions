package pythonrepl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mattsp1290/eino-agent/tools"
)

const (
	// ExecuteToolName is the stateful Python execution tool.
	ExecuteToolName = "python_repl"
	// ClearToolName discards one owner's live interpreter state.
	ClearToolName = "python_repl_clear"
	// DefaultOrder is used when Options.Order is zero.
	DefaultOrder = 100

	// ExecuteStatusCompleted means the snippet completed without a Python exception.
	ExecuteStatusCompleted = "completed"
	// ExecuteStatusPythonError means the snippet raised a Python exception.
	ExecuteStatusPythonError = "python_error"
	// StateResetCanceled means caller cancellation forced a fresh interpreter.
	StateResetCanceled = "canceled"
	// StateResetTimedOut means the execution deadline forced a fresh interpreter.
	StateResetTimedOut = "timed_out"
	// StateResetCleared means an explicit clear discarded interpreter state.
	StateResetCleared = "cleared"
	// StateResetRunnerFailed means a runner failure forced a fresh interpreter.
	StateResetRunnerFailed = "runner_failed"

	permissionExecute  = "process.python.execute"
	permissionManage   = "process.python.manage"
	maxClearInputBytes = 64
)

// BoundedText is an inline text field with an explicit truncation indicator.
type BoundedText struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// ExecuteResult is the bounded result of one Python request.
type ExecuteResult struct {
	// Status is ExecuteStatusCompleted or ExecuteStatusPythonError.
	Status     string      `json:"status"`
	Stdout     BoundedText `json:"stdout"`
	Stderr     BoundedText `json:"stderr"`
	Result     BoundedText `json:"result"`
	Exception  BoundedText `json:"exception"`
	Generation uint64      `json:"generation"`
	StateReset bool        `json:"state_reset"`
	// StateResetReason is empty or one of the exported StateReset constants.
	StateResetReason string `json:"state_reset_reason"`
}

// ClearResult describes whether clear invalidated live interpreter state.
type ClearResult struct {
	HadState   bool   `json:"had_state"`
	Generation uint64 `json:"generation"`
}

type executeInput struct {
	Code           string `json:"code" jsonschema:"required"`
	TimeoutSeconds *int64 `json:"timeout_seconds,omitempty"`
}

type clearInput struct{}

func normalizeExecute(options canonicalOptions) tools.InputNormalizer {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if uint64(len(raw)) > uint64(options.requestMax) {
			return nil, malformed("size")
		}
		var input executeInput
		if !strictDecode(raw, &input) {
			return nil, malformed("shape")
		}
		if input.Code == "" || !utf8.ValidString(input.Code) || len(input.Code) > options.limits.MaxCodeBytes {
			return nil, malformed("code")
		}
		if input.TimeoutSeconds != nil {
			seconds := *input.TimeoutSeconds
			if seconds < 0 || seconds > int64(options.limits.MaxTimeout/time.Second) {
				return nil, malformed("timeout")
			}
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, malformed("encoding")
		}
		return encoded, nil
	}
}

func normalizeClear(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) > maxClearInputBytes {
		return nil, malformed("size")
	}
	var input map[string]json.RawMessage
	if !strictDecode(raw, &input) || input == nil || len(input) != 0 {
		return nil, malformed("shape")
	}
	return json.RawMessage(`{}`), nil
}

func strictDecode(raw json.RawMessage, destination any) bool {
	if !utf8.Valid(raw) || !validJSONStructure(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(new(any)), io.EOF)
}

func malformed(code string) error {
	return errors.Join(tools.ErrMalformedInput, operationError("invalid-input-"+code))
}

func toolParameters(input any) *einoschema.ParamsOneOf {
	reflector := jsonschema.Reflector{Anonymous: true, DoNotReference: true, AllowAdditionalProperties: false}
	return einoschema.NewParamsOneOfByJSONSchema(reflector.Reflect(input))
}

func constantTypedPattern[I any](pattern string) tools.PermissionPattern {
	return tools.TypedPermissionPattern(func(ctx context.Context, _ I) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return pattern, nil
	})
}

func validResetReason(value string) bool {
	switch value {
	case "", StateResetCanceled, StateResetTimedOut, StateResetCleared, StateResetRunnerFailed:
		return true
	default:
		return false
	}
}

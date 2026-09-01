package backgroundjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mattsp1290/eino-agent/tools"
)

const (
	StartToolName  = "background_job_start"
	StatusToolName = "background_job_status"
	ListToolName   = "background_job_list"
	KillToolName   = "background_job_kill"
)

type JobState string

const (
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobKilled    JobState = "killed"
	JobTimedOut  JobState = "timed_out"
)

type TailResult struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type StartResult struct {
	ID             string   `json:"id"`
	State          JobState `json:"state"`
	StartedAt      string   `json:"started_at"`
	TimeoutSeconds int64    `json:"timeout_seconds"`
}

type StatusResult struct {
	ID             string     `json:"id"`
	State          JobState   `json:"state"`
	StartedAt      string     `json:"started_at"`
	CompletedAt    string     `json:"completed_at,omitempty"`
	TimeoutSeconds int64      `json:"timeout_seconds"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	Stdout         TailResult `json:"stdout"`
	Stderr         TailResult `json:"stderr"`
}

type JobSummary struct {
	ID             string   `json:"id"`
	State          JobState `json:"state"`
	StartedAt      string   `json:"started_at"`
	CompletedAt    string   `json:"completed_at,omitempty"`
	TimeoutSeconds int64    `json:"timeout_seconds"`
}

type ListResult struct {
	Jobs []JobSummary `json:"jobs"`
}

type KillResult struct {
	ID            string   `json:"id"`
	State         JobState `json:"state"`
	NewlyAccepted bool     `json:"newly_accepted"`
}

type startInput struct {
	Command          string `json:"command" jsonschema:"required"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	TimeoutSeconds   *int64 `json:"timeout_seconds,omitempty"`
}

type idInput struct {
	ID string `json:"id" jsonschema:"required"`
}

type listInput struct{}

func normalizeStart(options canonicalOptions) tools.InputNormalizer {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var input startInput
		if !strictDecode(raw, &input) {
			return nil, malformed("shape")
		}
		if input.Command == "" || len(input.Command) > options.limits.MaxCommandBytes || strings.IndexByte(input.Command, 0) >= 0 {
			return nil, malformed("command")
		}
		if input.WorkingDirectory == "" {
			input.WorkingDirectory = "."
		}
		if len(input.WorkingDirectory) > options.limits.MaxWorkingDirectoryBytes || strings.IndexByte(input.WorkingDirectory, 0) >= 0 || filepath.IsAbs(input.WorkingDirectory) {
			return nil, malformed("working-directory")
		}
		clean := filepath.Clean(input.WorkingDirectory)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, malformed("working-directory")
		}
		input.WorkingDirectory = clean
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

func normalizeID(_ canonicalOptions) tools.InputNormalizer {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var input idInput
		if !strictDecode(raw, &input) || !validJobID(input.ID) {
			return nil, malformed("id")
		}
		encoded, _ := json.Marshal(input)
		return encoded, nil
	}
}

func normalizeList(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var input listInput
	if !strictDecode(raw, &input) {
		return nil, malformed("shape")
	}
	return json.RawMessage(`{}`), nil
}

func strictDecode(raw json.RawMessage, destination any) bool {
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

func validJobID(id string) bool {
	if len(id) != 53 || !strings.HasPrefix(id, "job_") || id[36] != '_' {
		return false
	}
	for index, r := range id[4:] {
		if index == 32 {
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func toolParameters(input any) *einoschema.ParamsOneOf {
	reflector := jsonschema.Reflector{Anonymous: true, DoNotReference: true, AllowAdditionalProperties: false}
	return einoschema.NewParamsOneOfByJSONSchema(reflector.Reflect(input))
}

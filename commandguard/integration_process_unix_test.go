//go:build linux || darwin

package commandguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/backgroundjobs"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	"github.com/mattsp1290/eino-agent/tools/einotools"
	"github.com/mattsp1290/eino-tools/catalog"
	"github.com/mattsp1290/eino-tools/search"
	"github.com/mattsp1290/eino-tools/shell"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func ownMount(t *testing.T, m *composition.Mount) {
	t.Helper()
	t.Cleanup(func() {
		m.Deactivate()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
}
func (f *integrationFixture) standard(shellBinary string) {
	rg := filepath.Join(f.t.TempDir(), "rg")
	if err := os.WriteFile(rg, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		f.t.Fatal(err)
	}
	m, err := einotools.MountStandard(context.Background(), f.registry, fixtureComponent("standard-tools"), einotools.Options{Scope: extension.GlobalScope(), Permissions: map[string][]string{"standard.shell": {"fixture:execute"}}, Catalog: catalog.Options{SearchOptions: &search.Options{RGBinary: rg}, ShellOptions: &shell.Options{ShellBinary: shellBinary, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + f.t.TempDir(), "ENV=", "BASH_ENV="}}}})
	if err != nil {
		f.t.Fatal(err)
	}
	ownMount(f.t, m)
}
func (f *integrationFixture) background() {
	c := fixtureComponent("background-tools")
	c.Artifact.ConfigHash = ""
	m, err := backgroundjobs.Mount(context.Background(), f.registry, c, backgroundjobs.Options{ShellPath: "/bin/sh", ShellIdentity: "fixture-posix-shell", Environment: backgroundjobs.Environment{Mode: backgroundjobs.EnvironmentExplicitOnly, Identity: "fixture-env", Overrides: map[string]string{"PATH": "/usr/bin:/bin", "HOME": f.t.TempDir()}}, Limits: backgroundjobs.Limits{MaxRunning: 2, MaxTracked: 8, MaxCommandBytes: 4096, MaxWorkingDirectoryBytes: 1024, MaxOutputBytesPerStream: 4096, MaxEnvironmentEntries: 8, MaxEnvironmentBytes: 4096, MaxTimeout: 5 * time.Second, TerminateGrace: 100 * time.Millisecond, KillWait: time.Second}})
	if err != nil {
		f.t.Fatal(err)
	}
	ownMount(f.t, m)
}
func TestProductionStandardShellBinding(t *testing.T) {
	f := newIntegrationFixture(t)
	f.standard("/bin/sh")
	o := testOptions()
	o.Rules = []Rule{{ID: "canary", Executable: "printf", ArgPrefix: []string{"DENY"}}}
	f.guard(o)
	deniedPath, allowedPath := filepath.Join(f.workspace, "denied"), filepath.Join(f.workspace, "allowed")
	next := f.run(toolCall("denied", "shell", "cmd", "printf DENY > "+shellQuote(deniedPath)), toolCall("allowed", "shell", "cmd", "printf ALLOW > "+shellQuote(allowedPath)))
	if _, err := os.Stat(deniedPath); !os.IsNotExist(err) {
		t.Fatal("denied command wrote canary", err)
	}
	if data, err := os.ReadFile(allowedPath); err != nil || string(data) != "ALLOW" {
		t.Fatal("allowed shell did not execute", string(data), err)
	}
	if f.permissionCalls.Load() != 1 || f.call("denied").Status != session.ToolCallFailed || f.call("allowed").Status != session.ToolCallCompleted || !strings.Contains(string(next), ruleMatch.message()) {
		t.Fatal("production shell guard contract failed")
	}
}
func TestProductionBackgroundBinding(t *testing.T) {
	t.Run("denied creates no job", func(t *testing.T) {
		f := newIntegrationFixture(t)
		f.background()
		o := testOptions()
		o.Rules = []Rule{{ID: "canary", Executable: "printf"}}
		f.guard(o)
		canary := filepath.Join(f.workspace, "denied")
		list := einoschema.ToolCall{ID: "list", Type: "function", Function: einoschema.FunctionCall{Name: backgroundjobs.ListToolName, Arguments: `{}`}}
		f.run(toolCall("denied", backgroundjobs.StartToolName, "command", "printf DENY > "+shellQuote(canary)), list)
		if _, err := os.Stat(canary); !os.IsNotExist(err) {
			t.Fatal("denied job wrote canary", err)
		}
		var out runtime.ToolOutput
		var jobs backgroundjobs.ListResult
		if err := json.Unmarshal(f.call("list").Output, &out); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out.Structured, &jobs); err != nil {
			t.Fatal(err)
		}
		if len(jobs.Jobs) != 0 || f.permissionCalls.Load() != 1 || !strings.Contains(string(f.call("denied").Output), ruleMatch.message()) {
			t.Fatal("denied call created a job")
		}
	})
	t.Run("allowed starts and reaches terminal status", func(t *testing.T) {
		f := newIntegrationFixture(t)
		f.background()
		f.guard(testOptions())
		canary := filepath.Join(f.workspace, "allowed")
		phase := 0
		var jobID string
		polls := 0
		streamer := scriptedStreamer(func(_ context.Context, r model.Request) ([]*einoschema.Message, error) {
			var latest *einoschema.Message
			for _, m := range r.Messages {
				if m.Role == einoschema.Tool {
					latest = m
				}
			}
			if phase == 0 {
				phase = 1
				return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{toolCall("start", backgroundjobs.StartToolName, "command", "printf ALLOW > "+shellQuote(canary))})}, nil
			}
			if latest == nil {
				return nil, fmt.Errorf("missing job result")
			}
			var out runtime.ToolOutput
			if err := json.Unmarshal([]byte(latest.Content), &out); err != nil {
				return nil, err
			}
			if phase == 1 {
				var start backgroundjobs.StartResult
				if err := json.Unmarshal(out.Structured, &start); err != nil {
					return nil, err
				}
				jobID = start.ID
				phase = 2
			} else {
				var status backgroundjobs.StatusResult
				if err := json.Unmarshal(out.Structured, &status); err != nil {
					return nil, err
				}
				if status.State != backgroundjobs.JobRunning {
					if status.State != backgroundjobs.JobSucceeded || status.ExitCode == nil || *status.ExitCode != 0 {
						return nil, fmt.Errorf("job failed: %+v", status)
					}
					return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
				}
			}
			if jobID == "" || polls >= 10 {
				return nil, fmt.Errorf("job did not settle")
			}
			polls++
			time.Sleep(20 * time.Millisecond)
			return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{toolCall(fmt.Sprintf("poll-%d", polls), backgroundjobs.StatusToolName, "id", jobID)})}, nil
		})
		h, err := f.orchestrator(streamer).Start(context.Background(), f.request())
		if err != nil {
			t.Fatal(err)
		}
		waitRun(t, h)
		if data, err := os.ReadFile(canary); err != nil || string(data) != "ALLOW" {
			t.Fatal(string(data), err)
		}
		if jobID == "" || polls == 0 || f.permissionCalls.Load() < 2 {
			t.Fatal("job lifecycle not exercised")
		}
	})
}

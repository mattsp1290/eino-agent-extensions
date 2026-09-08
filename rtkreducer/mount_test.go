package rtkreducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

func executableOptions(t *testing.T) Options {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	options := testOptions()
	options.ExecutablePath = path
	options.ExecutableSHA256 = hex.EncodeToString(digest[:])
	options.TempRoot, err = filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func testComponent(configHash string) extension.Component {
	return extension.Component{
		InstanceID: "fixture-rtk-reducer",
		Artifact: extension.Artifact{
			Name: "rtk-reducer", Version: "test", Hash: "synthetic-artifact",
			ConfigHash: configHash, SourceKind: extension.SourceNative,
		},
	}
}

func TestMountRegistersAtomicTransformAndDerivesIdentity(t *testing.T) {
	options := executableOptions(t)
	wantHash, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, testComponent(""), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mount.Close(context.Background()) }()
	entries, err := os.ReadDir(options.TempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("mount created private resources before an eligible callback: %v", entries)
	}
	// Mount owns a frozen copy; later caller mutations cannot alter routing.
	options.Bindings[0].ToolName = "mutated-after-mount"
	options.Bindings[0].Fields[0].Filter = Log

	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	descriptor := plan.Descriptor()
	if len(descriptor.Components) != 1 || len(descriptor.Components[0].Handlers) != 1 {
		t.Fatalf("unexpected descriptor: %#v", descriptor.Components)
	}
	handler := descriptor.Components[0].Handlers[0]
	contract := runtime.ToolResultTransformPoint.Contract()
	if handler.ID != registrationID || handler.Order != DefaultOrder || handler.Scope != extension.GlobalScope() || handler.Contract != contract.ID || handler.Version != contract.Version {
		t.Fatalf("unexpected handler: %#v", handler)
	}
	if descriptor.Components[0].Artifact.ConfigHash != wantHash {
		t.Fatalf("config hash = %q, want %q", descriptor.Components[0].Artifact.ConfigHash, wantHash)
	}
}

func TestMountRejectsDigestAndFilesystemPolicyFailures(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Options)
		code string
	}{
		{"digest mismatch", func(value *Options) {
			value.ExecutableSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}, "executable-digest-mismatch"},
		{"missing executable", func(value *Options) { value.ExecutablePath = "/definitely/missing/rtk" }, "executable-path"},
		{"non-executable file", func(value *Options) {
			contents, err := os.ReadFile(value.ExecutablePath)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "rtk")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			value.ExecutablePath = path
		}, "executable-permission"},
		{"missing temp root", func(value *Options) { value.TempRoot = "/definitely/missing/tmp" }, "temp-root"},
		{"shared temp root", func(value *Options) {
			if err := os.Chmod(value.TempRoot, 0o777); err != nil {
				t.Fatal(err)
			}
		}, "temp-root"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := executableOptions(t)
			test.edit(&options)
			registry, err := composition.NewRegistry(nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Mount(context.Background(), registry, testComponent(""), options)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("mount error = %v, want code %q", err, test.code)
			}
		})
	}
}

func TestMountValidationIsAtomic(t *testing.T) {
	options := executableOptions(t)
	options.Bindings[0].Fields[0].Filter = "unknown"
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Mount(context.Background(), registry, testComponent(""), options); err == nil {
		t.Fatal("invalid policy mounted")
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	if len(plan.Descriptor().Components) != 0 {
		t.Fatal("failed mount published a component")
	}
}

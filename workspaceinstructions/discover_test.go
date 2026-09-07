package workspaceinstructions

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestDiscoverOrdersChainAndConfiguredNames(t *testing.T) {
	boundary := t.TempDir()
	middle := filepath.Join(boundary, "a")
	root := filepath.Join(middle, "b")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{boundary, middle, root} {
		for _, name := range []string{"LOCAL.md", "AGENTS.md"} {
			body := filepath.Base(directory) + ":" + name
			if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	workspace, err := resolveWorkspace(Workspace{Root: root, Boundary: boundary}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	files, err := discover(context.Background(), workspace, []string{"LOCAL.md", "AGENTS.md"}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wantDisplays := []string{"../../LOCAL.md", "../../AGENTS.md", "../LOCAL.md", "../AGENTS.md", "./LOCAL.md", "./AGENTS.md"}
	if len(files) != len(wantDisplays) {
		t.Fatalf("files = %#v", files)
	}
	for index, want := range wantDisplays {
		if files[index].display != want {
			t.Fatalf("display[%d] = %q, want %q", index, files[index].display, want)
		}
	}
}

func TestDiscoverSkipsInvalidCandidates(t *testing.T) {
	root := t.TempDir()
	entries := map[string][]byte{
		"valid": []byte("valid\r\nbody\r\n\t"), "nul": []byte("a\x00b"),
		"invalid": {0xff, 0xfe}, "blank": []byte(" \n\t"),
	}
	for name, data := range entries {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "valid"), filepath.Join(root, "inside-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	workspace, err := resolveWorkspace(Workspace{Root: root}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"missing", "directory", "inside-link", "outside-link", "nul", "invalid", "blank", "valid"}
	files, err := discover(context.Background(), workspace, names, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].display != "./valid" || files[0].content != "valid\r\nbody" {
		t.Fatalf("files = %#v", files)
	}
}

func TestDiscoverSkipsUnreadableFile(t *testing.T) {
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission semantics unavailable")
	}
	root := t.TempDir()
	name := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(name, []byte("secret"), 0); err != nil {
		t.Fatal(err)
	}
	workspace, err := resolveWorkspace(Workspace{Root: root}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	files, err := discover(context.Background(), workspace, []string{"AGENTS.md"}, testLimits())
	if err != nil || len(files) != 0 {
		t.Fatalf("files = %#v, err = %v", files, err)
	}
}

func TestDiscoverConfinesIntermediateSymlinkSwap(t *testing.T) {
	for _, test := range []struct {
		name       string
		inside     bool
		wantBodies int
	}{
		{"outside", false, 0}, {"inside", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary := t.TempDir()
			root := filepath.Join(boundary, "a", "b")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			workspace, err := resolveWorkspace(Workspace{Root: root, Boundary: boundary}, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(boundary, "old-a")
			if err := os.Rename(filepath.Join(boundary, "a"), old); err != nil {
				t.Fatal(err)
			}
			targetParent := t.TempDir()
			if test.inside {
				targetParent = filepath.Join(boundary, "replacement")
			}
			target := filepath.Join(targetParent, "b")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "AGENTS.md"), []byte("replacement-body"), 0o600); err != nil {
				t.Fatal(err)
			}
			linkTarget := targetParent
			if test.inside {
				linkTarget = "replacement"
			}
			if err := os.Symlink(linkTarget, filepath.Join(boundary, "a")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			files, err := discover(context.Background(), workspace, []string{"AGENTS.md"}, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != test.wantBodies {
				t.Fatalf("files = %#v", files)
			}
			if test.inside && files[0].content != "replacement-body" {
				t.Fatalf("content = %q", files[0].content)
			}
			for _, file := range files {
				if strings.Contains(file.content, "outside") {
					t.Fatal("outside content admitted")
				}
			}
		})
	}
}

func TestDiscoverTruncatesAtUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("abc€tail"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.MaxFileBytes = 4
	workspace, err := resolveWorkspace(Workspace{Root: root}, limits)
	if err != nil {
		t.Fatal(err)
	}
	files, err := discover(context.Background(), workspace, []string{"AGENTS.md"}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].content != "abc" || !files[0].truncated {
		t.Fatalf("files = %#v", files)
	}
}

func TestDiscoverCanceledBeforeRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := resolveWorkspace(Workspace{Root: root}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discover(ctx, workspace, []string{"AGENTS.md"}, testLimits()); err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
}

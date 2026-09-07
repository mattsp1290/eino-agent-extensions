package workspaceinstructions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceValidationAndChains(t *testing.T) {
	boundary := t.TempDir()
	middle := filepath.Join(boundary, "a")
	root := filepath.Join(middle, "b", "c")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(boundary, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sibling := t.TempDir()

	tests := []struct {
		name      string
		workspace Workspace
		code      string
	}{
		{"relative-root", Workspace{Root: "relative", Boundary: boundary}, "root"},
		{"missing-root", Workspace{Root: filepath.Join(boundary, "missing"), Boundary: boundary}, "root"},
		{"file-root", Workspace{Root: file, Boundary: boundary}, "root"},
		{"relative-boundary", Workspace{Root: root, Boundary: "relative"}, "boundary"},
		{"sibling-boundary", Workspace{Root: root, Boundary: sibling}, "boundary"},
		{"descendant-boundary", Workspace{Root: boundary, Boundary: root}, "boundary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveWorkspace(test.workspace, testLimits()); err == nil || !strings.Contains(err.Error(), "code="+test.code) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	resolved, err := resolveWorkspace(Workspace{Root: root, Boundary: boundary}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.chain, []string{".", "a", filepath.Join("a", "b"), filepath.Join("a", "b", "c")}) {
		t.Fatalf("chain = %#v", resolved.chain)
	}
	for name, workspace := range map[string]Workspace{
		"equal": {Root: root, Boundary: root}, "omitted": {Root: root},
	} {
		t.Run(name, func(t *testing.T) {
			got, resolveErr := resolveWorkspace(workspace, testLimits())
			if resolveErr != nil || !reflect.DeepEqual(got.chain, []string{"."}) {
				t.Fatalf("resolved = %#v, %v", got, resolveErr)
			}
		})
	}

	limits := testLimits()
	limits.MaxChainDepth = 3
	if _, err := resolveWorkspace(Workspace{Root: root, Boundary: boundary}, limits); err == nil || !strings.Contains(err.Error(), "code=chain-depth") {
		t.Fatalf("depth error = %v", err)
	}
}

func TestWorkspaceCanonicalizesSymlinkDirectories(t *testing.T) {
	if testing.Short() {
		t.Skip("filesystem symlink test")
	}
	realBoundary := t.TempDir()
	realRoot := filepath.Join(realBoundary, "project")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	boundaryLink := filepath.Join(linkParent, "boundary")
	rootLink := filepath.Join(realBoundary, "root-link")
	if err := os.Symlink(realBoundary, boundaryLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveWorkspace(Workspace{Root: rootLink, Boundary: boundaryLink}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, _ := filepath.EvalSymlinks(realRoot)
	wantBoundary, _ := filepath.EvalSymlinks(realBoundary)
	if resolved.root != wantRoot || resolved.boundary != wantBoundary || !reflect.DeepEqual(resolved.chain, []string{".", "project"}) {
		t.Fatalf("resolved = %#v", resolved)
	}
}

package workspaceinstructions

import (
	"os"
	"path/filepath"
	"strings"
)

type canonicalWorkspace struct {
	boundary     string
	boundaryInfo os.FileInfo
	chain        []string
}

func resolveWorkspace(workspace Workspace, limits Limits) (canonicalWorkspace, error) {
	root, _, err := canonicalDirectory(workspace.Root, "root")
	if err != nil {
		return canonicalWorkspace{}, err
	}
	boundaryInput := workspace.Boundary
	if boundaryInput == "" {
		boundaryInput = workspace.Root
	}
	boundary, boundaryInfo, err := canonicalDirectory(boundaryInput, "boundary")
	if err != nil {
		return canonicalWorkspace{}, err
	}
	rel, err := filepath.Rel(boundary, root)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return canonicalWorkspace{}, workspaceError("boundary")
	}
	chain := []string{"."}
	if rel != "." {
		parts := strings.Split(rel, string(filepath.Separator))
		chain = make([]string, 0, len(parts)+1)
		chain = append(chain, ".")
		current := ""
		for _, part := range parts {
			current = filepath.Join(current, part)
			chain = append(chain, current)
		}
	}
	if len(chain) > limits.MaxChainDepth {
		return canonicalWorkspace{}, workspaceError("chain-depth")
	}
	return canonicalWorkspace{boundary: boundary, boundaryInfo: boundaryInfo, chain: chain}, nil
}

func canonicalDirectory(value, code string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(value) {
		return "", nil, workspaceError(code)
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", nil, workspaceError(code)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, workspaceError(code)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", nil, workspaceError(code)
	}
	return canonical, info, nil
}

func openBoundary(workspace canonicalWorkspace) (*os.Root, error) {
	root, err := os.OpenRoot(workspace.boundary)
	if err != nil {
		return nil, workspaceError("boundary")
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(workspace.boundaryInfo, openedInfo) {
		_ = root.Close()
		return nil, workspaceError("boundary")
	}
	return root, nil
}

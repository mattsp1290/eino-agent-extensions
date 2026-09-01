package backgroundjobs

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveWorkingDirectory(workspaceRoot, relative string) (string, string, error) {
	if workspaceRoot == "" {
		return "", "", runtimeError("workspace-root")
	}
	root, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return "", "", runtimeError("workspace-root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", runtimeError("workspace-root")
	}
	rootInfo, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", "", runtimeError("workspace-root")
	}
	target := filepath.Join(rootInfo, relative)
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", "", runtimeError("working-directory")
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", runtimeError("working-directory")
	}
	rel, err := filepath.Rel(rootInfo, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", runtimeError("working-directory")
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return "", "", runtimeError("working-directory")
	}
	return rootInfo, target, nil
}

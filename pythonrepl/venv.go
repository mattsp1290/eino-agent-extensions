package pythonrepl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type virtualEnvironment struct {
	path           string
	interpreter    string
	remove         func(string) error
	finishCreation func() error
}

func canonicalWorkspaceRoot(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", runtimeError("workspace-root")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", runtimeError("workspace-root")
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", runtimeError("workspace-root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", runtimeError("workspace-root")
	}
	return resolved, nil
}

func withCeiling(parent context.Context, ceiling time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= ceiling {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, ceiling)
}

func removeVenv(venv *virtualEnvironment) error {
	if venv == nil || venv.path == "" {
		return nil
	}
	if venv.finishCreation != nil {
		if err := venv.finishCreation(); err != nil {
			return errCleanupIncomplete
		}
		venv.finishCreation = nil
	}
	remove := os.RemoveAll
	if venv.remove != nil {
		remove = venv.remove
	}
	if err := remove(venv.path); err != nil {
		return errCleanupIncomplete
	}
	if _, err := os.Lstat(venv.path); !errors.Is(err, os.ErrNotExist) {
		return errCleanupIncomplete
	}
	return nil
}

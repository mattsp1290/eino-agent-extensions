package workspaceinstructions

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type instructionFile struct {
	display   string
	content   string
	truncated bool
}

func walkInstructionFiles(ctx context.Context, workspace canonicalWorkspace, fileNames []string, limits Limits, reader candidateReader, visit func(instructionFile) bool) error {
	boundary, err := openBoundary(workspace)
	if err != nil {
		return err
	}
	defer func() {
		// All useful errors occur before this read-only capability is closed.
		_ = boundary.Close()
	}()

	for index, directory := range workspace.chain {
		for _, fileName := range fileNames {
			if err := ctx.Err(); err != nil {
				return err
			}
			candidate := filepath.Join(directory, fileName)
			info, err := boundary.Lstat(candidate)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			read, err := reader(ctx, boundary, candidate, limits.MaxFileBytes)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				continue
			}
			after, err := boundary.Lstat(candidate)
			if err != nil || !after.Mode().IsRegular() || read.info == nil || !read.info.Mode().IsRegular() ||
				!os.SameFile(info, read.info) || !os.SameFile(read.info, after) {
				continue
			}
			content, truncated, admitted := admitContent(read.data, read.info.Size() > int64(limits.MaxFileBytes), limits.MaxFileBytes)
			if !admitted {
				continue
			}
			if !visit(instructionFile{
				display: displayPath(len(workspace.chain), index, fileName),
				content: content, truncated: truncated,
			}) {
				return nil
			}
		}
	}
	return nil
}

func admitContent(data []byte, sizeExceeded bool, maxBytes int) (string, bool, bool) {
	truncated := sizeExceeded || len(data) > maxBytes
	if len(data) > maxBytes {
		data = data[:maxBytes]
	}
	if !utf8.Valid(data) {
		if !truncated {
			return "", false, false
		}
		valid := false
		minimum := len(data) - utf8.UTFMax + 1
		if minimum < 0 {
			minimum = 0
		}
		for end := len(data) - 1; end >= minimum; end-- {
			if utf8.Valid(data[:end]) {
				data = data[:end]
				valid = true
				break
			}
		}
		if !valid {
			return "", false, false
		}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false, false
	}
	content := strings.TrimRightFunc(string(data), unicode.IsSpace)
	if content == "" {
		return "", false, false
	}
	return content, truncated, true
}

func displayPath(chainLength, index int, fileName string) string {
	if index == chainLength-1 {
		return "./" + fileName
	}
	return strings.Repeat("../", chainLength-1-index) + fileName
}

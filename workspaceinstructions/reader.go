package workspaceinstructions

import (
	"context"
	"errors"
	"io"
	"os"
)

type candidateRead struct {
	data []byte
	info os.FileInfo
}

type candidateReader func(context.Context, *os.Root, string, int) (candidateRead, error)

func defaultReadCandidate(ctx context.Context, root *os.Root, name string, maxBytes int) (candidateRead, error) {
	if err := ctx.Err(); err != nil {
		return candidateRead{}, err
	}
	// Root.Open may follow a final symlink. Discovery's Lstat/File.Stat/Lstat
	// identity checks are the canonical defense: stable symlinks fail the first
	// check, and substitutions cannot preserve all three file identities.
	file, err := root.Open(name)
	if err != nil {
		return candidateRead{}, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return candidateRead{}, err
	}
	data := make([]byte, maxBytes+1)
	count := 0
	for count < len(data) {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return candidateRead{}, err
		}
		n, readErr := file.Read(data[count:])
		count += n
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = file.Close()
				return candidateRead{}, readErr
			}
			break
		}
		if n == 0 {
			break
		}
	}
	if err := file.Close(); err != nil {
		return candidateRead{}, err
	}
	return candidateRead{data: data[:count], info: info}, nil
}

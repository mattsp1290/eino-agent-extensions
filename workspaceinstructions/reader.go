package workspaceinstructions

import (
	"context"
	"io"
	"os"
)

type candidateRead struct {
	data []byte
	info os.FileInfo
}

type candidateReader func(context.Context, *os.Root, string, int) (candidateRead, error)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.r.Read(buffer)
}

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
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: file}, int64(maxBytes)+1))
	closeErr := file.Close()
	if readErr != nil {
		return candidateRead{}, readErr
	}
	if closeErr != nil {
		return candidateRead{}, closeErr
	}
	return candidateRead{data: data, info: info}, nil
}

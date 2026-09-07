package workspaceinstructions

import (
	"context"
	"errors"
	"io"
	"os"
)

// readCandidate is package-private so tests can force a blocked filesystem
// boundary without making a test hook part of mounted behavior or identity.
var readCandidate = defaultReadCandidate

func defaultReadCandidate(ctx context.Context, root *os.Root, name string, maxBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	data := make([]byte, maxBytes+1)
	count := 0
	for count < len(data) {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		n, readErr := file.Read(data[count:])
		count += n
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = file.Close()
				return nil, readErr
			}
			break
		}
		if n == 0 {
			break
		}
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return data[:count], nil
}

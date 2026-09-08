//go:build linux || darwin

package rtkreducer

import (
	"os"
	"runtime"
	"syscall"
)

func platformSupported() bool { return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" }

func platformPathControlled(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

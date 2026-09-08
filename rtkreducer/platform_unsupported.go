//go:build !linux && !darwin

package rtkreducer

import "os"

func platformSupported() bool { return false }

func platformPathControlled(os.FileInfo) bool { return false }

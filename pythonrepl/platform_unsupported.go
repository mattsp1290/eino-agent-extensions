//go:build !linux && !darwin

package pythonrepl

func platformSupported() bool { return false }

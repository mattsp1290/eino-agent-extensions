//go:build !linux && !darwin

package backgroundjobs

import "os"

func prepareProcess(canonicalOptions, string, string, *tailWriter, *tailWriter) (*preparedProcess, error) {
	return nil, configError("unsupported-platform")
}

func signalProcessGroup(int, os.Signal) error { return configError("unsupported-platform") }
func termSignal() os.Signal                   { return nil }
func killSignal() os.Signal                   { return nil }

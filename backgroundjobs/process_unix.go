//go:build linux || darwin

package backgroundjobs

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareProcess(options canonicalOptions, directory, command string, stdout, stderr *tailWriter) (*preparedProcess, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, operationError("spawn-failed")
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, operationError("spawn-failed")
	}
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = readyReader.Close()
		_ = readyWriter.Close()
		return nil, operationError("spawn-failed")
	}
	cmd := exec.Command(options.shellPath, "-c", fixedSupervisorScript,
		"background-job-supervisor", options.shellPath, command)
	cmd.Dir = directory
	cmd.Env = append([]string(nil), options.env...)
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{writer, readyWriter, controlReader}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = options.limits.KillWait
	return &preparedProcess{
		cmd: cmd, statusReader: reader, statusWriter: writer,
		readyReader: readyReader, readyWriter: readyWriter,
		controlReader: controlReader, controlWriter: controlWriter,
	}, nil
}

func signalProcessGroup(pgid int, signal os.Signal) error {
	systemSignal, ok := signal.(syscall.Signal)
	if !ok || pgid <= 0 {
		return errors.New("invalid process group signal")
	}
	return syscall.Kill(-pgid, systemSignal)
}

func termSignal() os.Signal { return syscall.SIGTERM }
func killSignal() os.Signal { return syscall.SIGKILL }

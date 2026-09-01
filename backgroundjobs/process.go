package backgroundjobs

import (
	"io"
	"os"
	"os/exec"
)

// The requested command is passed as an argument, never interpolated here.
// FD 3 carries final status, FD 4 carries readiness, and FD 5 is the manager's
// one-way launch gate. All are package-private and closed before nested exec.
const fixedSupervisorScript = `trap '' TERM
printf R >&4
exec 4>&-
if IFS= read -r gate <&5 && [ "$gate" = G ]; then
	exec 5<&-
else
	exec 5<&-
	while :; do
		sleep 2147483647 </dev/null >/dev/null 2>&1 &
		wait $!
	done
fi
shell=$1
command=$2
(
	trap - TERM
	exec 3>&-
	exec 4>&-
	exec 5<&-
	exec "$shell" -c "$command"
)
status=$?
printf 'v1:%d\n' "$status" >&3
exec 3>&-
while :; do
	sleep 2147483647 </dev/null >/dev/null 2>&1 &
	wait $!
done
`

type preparedProcess struct {
	cmd           *exec.Cmd
	statusReader  io.ReadCloser
	statusWriter  *os.File
	readyReader   *os.File
	readyWriter   *os.File
	controlReader *os.File
	controlWriter *os.File
}

func (process *preparedProcess) closeBeforeStart() {
	_ = process.statusReader.Close()
	_ = process.statusWriter.Close()
	_ = process.readyReader.Close()
	_ = process.readyWriter.Close()
	_ = process.controlReader.Close()
	_ = process.controlWriter.Close()
}

func (process *preparedProcess) parentAfterStart() {
	_ = process.statusWriter.Close()
	_ = process.readyWriter.Close()
	_ = process.controlReader.Close()
}

func (process *preparedProcess) releaseCommand(release bool) error {
	defer process.controlWriter.Close()
	if !release {
		return nil
	}
	_, err := process.controlWriter.Write([]byte("G\n"))
	return err
}

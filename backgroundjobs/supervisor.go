package backgroundjobs

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func waitSupervisorReady(reader *os.File, limit time.Duration) (resultErr error) {
	defer func() {
		if err := reader.Close(); resultErr == nil && err != nil {
			resultErr = operationError("supervisor-readiness")
		}
	}()
	if err := reader.SetReadDeadline(time.Now().Add(limit)); err != nil {
		return operationError("supervisor-readiness")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 2))
	if err != nil || string(raw) != "R" {
		return operationError("supervisor-readiness")
	}
	return nil
}

func (job *job) readStatus(reader io.ReadCloser) {
	defer reader.Close()
	limited := bufio.NewReader(io.LimitReader(reader, 65))
	raw, err := io.ReadAll(limited)
	valid, code := parseSupervisorStatus(raw, err)
	job.mu.Lock()
	job.statusValid = valid
	job.statusCode = code
	job.statusOnce.Do(func() { close(job.statusReady) })
	if job.cause == causeNone {
		job.cause = causeNatural
		job.startCoordinatorLocked()
	}
	job.mu.Unlock()
}

func parseSupervisorStatus(raw []byte, readErr error) (bool, int) {
	if readErr != nil || len(raw) < 5 || len(raw) > 8 || !strings.HasPrefix(string(raw), "v1:") || raw[len(raw)-1] != '\n' {
		return false, 0
	}
	value := string(raw[3 : len(raw)-1])
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false, 0
	}
	code, err := strconv.Atoi(value)
	return err == nil && code >= 0 && code <= 255, code
}

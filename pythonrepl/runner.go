package pythonrepl

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"unicode/utf8"
)

const fixedRunnerSource = `
import ast, builtins, io, json, os, struct, sys, traceback

PROTOCOL = sys.argv[1]
REQUEST_MAX = int(sys.argv[2])
RESPONSE_MAX = int(sys.argv[3])
STDOUT_MAX = int(sys.argv[4])
STDERR_MAX = int(sys.argv[5])
RESULT_MAX = int(sys.argv[6])
EXCEPTION_MAX = int(sys.argv[7])
REQUEST_FD = 3
RESPONSE_FD = 4
os.set_inheritable(REQUEST_FD, False)
os.set_inheritable(RESPONSE_FD, False)

class BoundedWriter(io.TextIOBase):
    def __init__(self, maximum):
        self.maximum = maximum
        self.parts = []
        self.used = 0
        self.truncated = False
        self.active = True
    def writable(self):
        return True
    def write(self, value):
        if not isinstance(value, str):
            value = str(value)
        accepted = len(value)
        if not self.active or self.truncated:
            return accepted
        raw = value.encode("utf-8", "replace")
        room = self.maximum - self.used
        if len(raw) > room:
            raw = raw[:max(room, 0)]
            while raw:
                try:
                    value = raw.decode("utf-8")
                    break
                except UnicodeDecodeError:
                    raw = raw[:-1]
            else:
                value = ""
            self.truncated = True
        else:
            value = raw.decode("utf-8")
        if raw:
            self.parts.append(value)
            self.used += len(raw)
        return accepted
    def flush(self):
        return None
    def seal(self):
        self.active = False
    def result(self):
        return {"text": "".join(self.parts), "truncated": self.truncated}

class DiscardWriter(io.TextIOBase):
    def writable(self):
        return True
    def write(self, value):
        return len(value) if isinstance(value, str) else 0
    def flush(self):
        return None

def read_exact(fd, size):
    chunks = []
    remaining = size
    while remaining:
        chunk = os.read(fd, remaining)
        if not chunk:
            raise EOFError()
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)

def read_frame():
    header = read_exact(REQUEST_FD, 4)
    size = struct.unpack(">I", header)[0]
    if size == 0 or size > REQUEST_MAX:
        raise RuntimeError("invalid request frame")
    raw = read_exact(REQUEST_FD, size)
    return json.loads(raw.decode("utf-8"))

def write_all(fd, raw):
    offset = 0
    while offset < len(raw):
        offset += os.write(fd, raw[offset:])

def write_frame(value):
    raw = json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if not raw or len(raw) > RESPONSE_MAX:
        raise RuntimeError("invalid response frame")
    write_all(RESPONSE_FD, struct.pack(">I", len(raw)) + raw)

def bounded(value, maximum):
    raw = value.encode("utf-8", "replace")
    if len(raw) <= maximum:
        return {"text": raw.decode("utf-8"), "truncated": False}
    raw = raw[:maximum]
    while raw:
        try:
            return {"text": raw.decode("utf-8"), "truncated": True}
        except UnicodeDecodeError:
            raw = raw[:-1]
    return {"text": "", "truncated": True}

user_globals = {"__builtins__": builtins, "__name__": "__main__"}
discard = DiscardWriter()
sys.stdout = discard
sys.stderr = discard

while True:
    try:
        request = read_frame()
    except EOFError:
        break
    if not isinstance(request, dict) or set(request) != {"version", "id", "code"}:
        raise RuntimeError("invalid request")
    if request["version"] != PROTOCOL or not isinstance(request["id"], int) or request["id"] <= 0 or not isinstance(request["code"], str):
        raise RuntimeError("invalid request")
    stdout = BoundedWriter(STDOUT_MAX)
    stderr = BoundedWriter(STDERR_MAX)
    sys.stdout = stdout
    sys.stderr = stderr
    status = "completed"
    result = {"text": "", "truncated": False}
    exception = {"text": "", "truncated": False}
    try:
        tree = ast.parse(request["code"], filename="<python_repl>", mode="exec")
        if tree.body and isinstance(tree.body[-1], ast.Expr):
            prefix = ast.Module(body=tree.body[:-1], type_ignores=[])
            if prefix.body:
                exec(compile(prefix, "<python_repl>", "exec"), user_globals, user_globals)
            value = eval(compile(ast.Expression(tree.body[-1].value), "<python_repl>", "eval"), user_globals, user_globals)
            result = bounded(repr(value), RESULT_MAX)
        else:
            exec(compile(tree, "<python_repl>", "exec"), user_globals, user_globals)
    except BaseException as exc:
        status = "python_error"
        user_tb = exc.__traceback__
        while user_tb is not None and user_tb.tb_frame.f_code.co_filename != "<python_repl>":
            user_tb = user_tb.tb_next
        rendered = []
        if user_tb is not None:
            rendered.append("Traceback (most recent call last):\n")
            rendered.extend(traceback.format_list(traceback.extract_tb(user_tb)))
        rendered.extend(traceback.format_exception_only(type(exc), exc))
        exception = bounded("".join(rendered), EXCEPTION_MAX)
    finally:
        stdout.seal()
        stderr.seal()
        sys.stdout = discard
        sys.stderr = discard
    write_frame({
        "version": PROTOCOL, "id": request["id"], "status": status,
        "stdout": stdout.result(), "stderr": stderr.result(),
        "result": result, "exception": exception,
    })
`

const fixedSupervisorSource = `
import json, os, struct, subprocess, sys

def write_all(fd, raw):
    offset = 0
    while offset < len(raw):
        offset += os.write(fd, raw[offset:])

def write_frame(fd, value):
    raw = json.dumps(value, separators=(",", ":")).encode("utf-8")
    write_all(fd, struct.pack(">I", len(raw)) + raw)

def read_exact(fd, size):
    chunks = []
    while size:
        chunk = os.read(fd, size)
        if not chunk:
            raise EOFError()
        chunks.append(chunk)
        size -= len(chunk)
    return b"".join(chunks)

if sys.argv[1] == "--venv-create":
    import venv
    VENV_STATUS_FD, VENV_HOLD_FD = 3, 4
    succeeded = True
    try:
        venv.EnvBuilder(with_pip=False).create(sys.argv[2])
    except BaseException:
        succeeded = False
    write_frame(VENV_STATUS_FD, {"succeeded": succeeded})
    os.read(VENV_HOLD_FD, 1)
else:
    RUNNER = sys.argv[1]
    PROTOCOL = sys.argv[2]
    RUNNER_ARGS = sys.argv[2:]
    REQUEST_FD, RESPONSE_FD, STATUS_FD, CONTROL_FD = 3, 4, 5, 6

    def read_control():
        size = struct.unpack(">I", read_exact(CONTROL_FD, 4))[0]
        if size == 0 or size > 32:
            raise RuntimeError("invalid control")
        value = json.loads(read_exact(CONTROL_FD, size).decode("utf-8"))
        if value != {"command": "reap"}:
            raise RuntimeError("invalid control")

    runner = subprocess.Popen(
        [sys.executable, "-I", "-u", "-c", RUNNER] + RUNNER_ARGS,
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        close_fds=True, pass_fds=(REQUEST_FD, RESPONSE_FD), start_new_session=True,
    )
    os.close(REQUEST_FD)
    os.close(RESPONSE_FD)
    write_frame(STATUS_FD, {
        "phase": "ready", "version": PROTOCOL, "pid": runner.pid,
        "pgid": os.getpgid(runner.pid), "python": [sys.version_info.major, sys.version_info.minor],
    })
    read_control()
    code = runner.wait()
    write_frame(STATUS_FD, {"phase": "reaped", "version": PROTOCOL, "exit_code": code})
    os.close(STATUS_FD)
    os.close(CONTROL_FD)
`

type runnerRequest struct {
	Version string `json:"version"`
	ID      uint64 `json:"id"`
	Code    string `json:"code"`
}

type runnerResponse struct {
	Version   string      `json:"version"`
	ID        uint64      `json:"id"`
	Status    string      `json:"status"`
	Stdout    BoundedText `json:"stdout"`
	Stderr    BoundedText `json:"stderr"`
	Result    BoundedText `json:"result"`
	Exception BoundedText `json:"exception"`
}

type supervisorStatus struct {
	Phase    string `json:"phase"`
	Version  string `json:"version"`
	PID      int    `json:"pid,omitempty"`
	PGID     int    `json:"pgid,omitempty"`
	Python   []int  `json:"python,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

type executionOutcome struct {
	response        runnerResponse
	mayHaveExecuted bool
}

type raceCoordinator struct {
	mu        sync.Mutex
	canceled  bool
	committed bool
	cancelCh  chan struct{}
}

func newRaceCoordinator(ctx context.Context) (*raceCoordinator, func() bool) {
	coordinator := &raceCoordinator{cancelCh: make(chan struct{})}
	stop := context.AfterFunc(ctx, func() {
		coordinator.mu.Lock()
		if !coordinator.committed && !coordinator.canceled {
			coordinator.canceled = true
			close(coordinator.cancelCh)
		}
		coordinator.mu.Unlock()
	})
	return coordinator, stop
}

func (coordinator *raceCoordinator) commit() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.canceled {
		return false
	}
	coordinator.committed = true
	return true
}

func (coordinator *raceCoordinator) interrupt() {
	coordinator.mu.Lock()
	if !coordinator.committed && !coordinator.canceled {
		coordinator.canceled = true
		close(coordinator.cancelCh)
	}
	coordinator.mu.Unlock()
}

func encodeFrame(value any, maximum uint32) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || uint64(len(body)) > uint64(maximum) {
		return nil, operationError("protocol-encode")
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	return frame, nil
}

func readFrame(reader io.Reader, maximum uint32, destination any) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return operationError("protocol-read")
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maximum {
		return operationError("protocol-frame")
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(reader, body); err != nil {
		return operationError("protocol-read")
	}
	if !utf8.Valid(body) || !validJSONStructure(body) {
		return operationError("protocol-json")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return operationError("protocol-json")
	}
	return nil
}

func validJSONStructure(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return false
				}
				if _, duplicate := seen[key]; duplicate {
					return false
				}
				seen[key] = struct{}{}
				if !value() {
					return false
				}
			}
			closing, err := decoder.Token()
			return err == nil && closing == json.Delim('}')
		case '[':
			for decoder.More() {
				if !value() {
					return false
				}
			}
			closing, err := decoder.Token()
			return err == nil && closing == json.Delim(']')
		default:
			return false
		}
	}
	if !value() {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

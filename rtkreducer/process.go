package rtkreducer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type invocationDirs struct {
	root   string
	work   string
	home   string
	config string
	data   string
	claude string
	tmp    string
}

func (d invocationDirs) env() []string {
	// This list is intentionally constructed from scratch. In particular, PATH,
	// proxy settings, credentials and dynamic-loader variables do not cross the
	// native boundary.
	return []string{
		"HOME=" + d.home,
		"XDG_CONFIG_HOME=" + d.config,
		"XDG_DATA_HOME=" + d.data,
		"CLAUDE_CONFIG_DIR=" + d.claude,
		"TMPDIR=" + d.tmp,
		"RTK_TELEMETRY_DISABLED=1",
		"NO_COLOR=1",
		"TERM=dumb",
		"LANG=C",
		"LC_ALL=C",
	}
}

func makeInvocation(root string) (invocationDirs, error) {
	path, err := os.MkdirTemp(root, "invocation-")
	if err != nil {
		return invocationDirs{}, err
	}
	dirs := invocationDirs{
		root:   path,
		work:   filepath.Join(path, "work"),
		home:   filepath.Join(path, "home"),
		config: filepath.Join(path, "config"),
		data:   filepath.Join(path, "data"),
		claude: filepath.Join(path, "claude"),
		tmp:    filepath.Join(path, "tmp"),
	}
	for _, dir := range []string{dirs.work, dirs.home, dirs.config, dirs.data, dirs.claude, dirs.tmp} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return dirs, err
		}
	}
	return dirs, nil
}

type coordinator struct {
	executablePath string
	tempRoot       string
	limits         Limits

	mu          sync.Mutex
	root        string
	quarantined bool
	closing     bool
	closed      bool
	active      int
	paths       map[string]struct{}
	change      chan struct{}
	slots       chan struct{}
	// inspectInvocation is a test-only observation hook. Production never sets
	// it; it cannot alter public configuration identity or process behavior.
	inspectInvocation func(invocationDirs)
}

// newCoordinator freezes the process-lane values needed after mount. The
// canonical policy is already immutable; this copy prevents later caller
// mutation from changing process behavior.
func newCoordinator(options canonicalOptions) *coordinator {
	return &coordinator{
		executablePath: options.executablePath,
		tempRoot:       options.tempRoot,
		limits:         options.limits,
		paths:          make(map[string]struct{}),
		change:         make(chan struct{}),
		slots:          make(chan struct{}, options.limits.MaxConcurrent),
	}
}

// acquire reserves one callback slot without waiting. The caller must retain
// the lease across every field in that callback and release it exactly once.
func (c *coordinator) acquire(ctx context.Context) (*engineLease, bool) {
	if c == nil {
		return nil, false
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, false
	}
	select {
	case c.slots <- struct{}{}:
	default:
		return nil, false
	}
	c.mu.Lock()
	if c.closing || c.closed || c.quarantined {
		c.mu.Unlock()
		<-c.slots
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leaseContext, leaseCancel := context.WithTimeout(ctx, c.limits.Timeout)
	c.active++
	c.mu.Unlock()
	return &engineLease{coordinator: c, context: leaseContext, cancel: leaseCancel}, true
}

// reduce is a convenience for private callers that have one field. Transform
// code should use acquire so one callback consumes one capacity slot.
func (c *coordinator) reduce(ctx context.Context, filter Filter, input string) (output string, ok bool) {
	lease, ok := c.acquire(ctx)
	if !ok {
		return "", false
	}
	defer lease.release()
	return lease.reduce(ctx, filter, input)
}

func (c *coordinator) lazyRoot() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.closed || c.quarantined {
		return "", false
	}
	if c.root != "" {
		return c.root, true
	}
	root, err := os.MkdirTemp(c.tempRoot, ".rtk-reducer-")
	if err != nil {
		c.quarantined = true
		return "", false
	}
	if err := os.Chmod(root, 0700); err != nil {
		c.root = root
		if removeErr := os.RemoveAll(root); removeErr != nil {
			c.paths[root] = struct{}{}
		}
		c.quarantined = true
		return "", false
	}
	c.root = root
	return root, true
}

func (c *coordinator) finishOwner() {
	c.mu.Lock()
	c.active--
	close(c.change)
	c.change = make(chan struct{})
	c.mu.Unlock()
}

func (c *coordinator) releaseSlot() {
	<-c.slots
}

func (c *coordinator) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil
		}
		c.closing = true
		if c.active != 0 {
			change := c.change
			c.mu.Unlock()
			select {
			case <-change:
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		paths := make([]string, 0, len(c.paths))
		for path := range c.paths {
			paths = append(paths, path)
		}
		root := c.root
		c.mu.Unlock()

		failed := false
		for _, path := range paths {
			if err := os.RemoveAll(path); err != nil {
				failed = true
				continue
			}
			c.mu.Lock()
			delete(c.paths, path)
			c.mu.Unlock()
		}
		if root != "" {
			if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
				failed = true
			} else {
				c.mu.Lock()
				c.root = ""
				c.mu.Unlock()
			}
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil
		}
		remaining := len(c.paths)
		if remaining == 0 && c.root == "" && !failed {
			c.closed = true
			close(c.change)
			c.mu.Unlock()
			return nil
		}
		c.quarantined = true
		c.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return cleanupError()
	}
}

type engineLease struct {
	coordinator *coordinator
	context     context.Context

	mu       sync.Mutex
	pending  int
	released bool
	cancel   context.CancelFunc
}

func (l *engineLease) hold() {
	l.mu.Lock()
	l.pending++
	l.mu.Unlock()
}

func (l *engineLease) ownerDone() {
	l.mu.Lock()
	l.pending--
	release := l.released && l.pending == 0
	l.mu.Unlock()
	if release {
		l.coordinator.finishOwner()
		l.coordinator.releaseSlot()
	}
}

func (l *engineLease) release() {
	if l == nil || l.coordinator == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	release := l.pending == 0
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if release {
		l.coordinator.finishOwner()
		l.coordinator.releaseSlot()
	}
}

type processResult struct {
	output string
	ok     bool
}

type processOwner struct {
	lease    *engineLease
	coord    *coordinator
	dirs     invocationDirs
	cancelMu sync.Mutex
	cancel   context.CancelFunc
	done     chan processResult
}

func (l *engineLease) reduce(ctx context.Context, filter Filter, input string) (string, bool) {
	if l == nil || l.coordinator == nil || !validFilter(filter) || input == "" || !utf8.ValidString(input) || len(input) > l.coordinator.limits.MaxFieldBytes {
		return "", false
	}
	runContext := l.context
	if runContext == nil {
		runContext = ctx
		if runContext == nil {
			runContext = context.Background()
		}
	}
	if err := runContext.Err(); err != nil {
		return "", false
	}
	owner := &processOwner{lease: l, coord: l.coordinator, done: make(chan processResult, 1)}
	l.hold()
	go owner.run(runContext, filter, input)

	select {
	case result := <-owner.done:
		return result.output, result.ok
	case <-runContext.Done():
		owner.cancelProcess()
		wait := time.NewTimer(l.coordinator.limits.KillWait)
		defer wait.Stop()
		select {
		case result := <-owner.done:
			return result.output, result.ok
		case <-wait.C:
			// The owner goroutine remains responsible for Wait, stream drains and
			// directory removal. Its lease hold keeps capacity reserved.
			return "", false
		}
	}
}

func (o *processOwner) cancelProcess() {
	if o == nil {
		return
	}
	o.cancelMu.Lock()
	cancel := o.cancel
	o.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (o *processOwner) setCancel(cancel context.CancelFunc) {
	o.cancelMu.Lock()
	o.cancel = cancel
	o.cancelMu.Unlock()
}

func (o *processOwner) run(parent context.Context, filter Filter, input string) {
	result := processResult{}
	defer func() {
		if recover() != nil {
			result = processResult{}
		}
		if o.dirs.root != "" {
			if err := os.RemoveAll(o.dirs.root); err != nil {
				result = processResult{}
				o.coord.mu.Lock()
				o.coord.paths[o.dirs.root] = struct{}{}
				o.coord.quarantined = true
				o.coord.mu.Unlock()
			}
		}
		o.lease.ownerDone()
		o.done <- result
	}()

	root, ok := o.coord.lazyRoot()
	if !ok {
		return
	}
	dirs, err := makeInvocation(root)
	o.dirs = dirs
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(parent)
	o.setCancel(cancel)
	defer cancel()
	result.output, result.ok = runProcess(ctx, o.coord.executablePath, o.coord.limits, dirs, filter, input)
	if inspect := o.coord.inspectInvocation; inspect != nil {
		inspect(dirs)
	}
}

func runProcess(ctx context.Context, executable string, limits Limits, dirs invocationDirs, filter Filter, input string) (string, bool) {
	if ctx == nil || executable == "" || !validFilter(filter) || input == "" || len(input) > limits.MaxFieldBytes {
		return "", false
	}
	cmd := exec.CommandContext(ctx, executable, "pipe", "--filter", string(filter))
	cmd.Dir = dirs.work
	cmd.Env = dirs.env()
	cmd.WaitDelay = limits.KillWait
	if err := configureProcess(cmd); err != nil {
		return "", false
	}
	stdoutCapture := newBoundedCapture(limits.MaxStdoutBytes)
	stderrCapture := newDiscardCapture(limits.MaxStderrBytes)
	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = captureWriter{capture: stdoutCapture, overflow: stop}
	cmd.Stderr = captureWriter{capture: stderrCapture, overflow: stop}
	// os/exec now owns and joins every pipe-copy goroutine before Run returns.
	// This avoids racing custom StdoutPipe readers with Wait closing the files;
	// WaitDelay bounds any copy that remains stuck after cancellation or exit.
	runErr := cmd.Run()
	if runErr != nil || ctx.Err() != nil || stdoutCapture.overflow || stderrCapture.overflow {
		return "", false
	}
	output := stdoutCapture.bytes()
	if len(output) == 0 || !utf8.Valid(output) {
		return "", false
	}
	return string(output), true
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mudler/xlog"

	"github.com/mudler/LocalAI/pkg/httpclient"
)

// engineConfig is everything needed to start and reach one radiance engine.
type engineConfig struct {
	Path         string        // engine executable
	ConfigPath   string        // radiance.yaml handed to --config
	Env          []string      // extra KEY=VALUE entries; they override os.Environ()
	ReadyTimeout time.Duration // how long to wait for GET /health to return 200
	StopTimeout  time.Duration // how long the engine may take to exit after SIGTERM
	BaseURL      string        // http://host:port of the engine
	Client       *http.Client  // hardened client used for the readiness probe
}

// engine owns the radiance child process and its bounded log tail.
type engine struct {
	cfg engineConfig
	cmd *exec.Cmd

	exited  chan struct{}
	waitErr error

	tail   *tailBuffer
	stdout *lineWriter
	stderr *lineWriter

	mu      sync.Mutex
	killing bool
}

const (
	engineTailLines = 50
	engineTailBytes = 4096

	// engineReapTimeout bounds the wait for Cmd.Wait after a kill. A helper
	// process that inherited the engine's stdout/stderr can keep Cmd.Wait
	// waiting on the copy goroutines even after the direct child is dead;
	// Free and Load must still return.
	engineReapTimeout = 5 * time.Second
)

// startEngine starts the engine and waits until its health endpoint reports
// ready. A timeout or an early exit kills the child and returns an error that
// carries its last stderr lines.
func startEngine(cfg engineConfig) (*engine, error) {
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = defaultStopTimeout
	}
	if cfg.Client == nil {
		cfg.Client = httpclient.New()
	}
	if cfg.ConfigPath == "" {
		return nil, errors.New("radiance: engine config path is empty")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("radiance: engine base URL is empty")
	}

	path, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("radiance: engine %q: %w", cfg.Path, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("radiance: engine %q is not available: %w", cfg.Path, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("radiance: engine %q is a directory", path)
	}

	cmd := exec.Command(path, "--config", cfg.ConfigPath) // #nosec G204 -- absolute engine binary from the model's engine_path option, no shell, argv only
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)
	setChildAttrs(cmd)

	e := &engine{
		cfg:    cfg,
		cmd:    cmd,
		exited: make(chan struct{}),
		tail:   &tailBuffer{maxLines: engineTailLines, maxBytes: engineTailBytes},
	}
	// Writers rather than StdoutPipe: Cmd.Wait then waits for the copies to
	// finish, so the last stderr lines are in the tail before the error is
	// built. Reading a StdoutPipe concurrently with Wait can lose them.
	e.stdout = newLineWriter(e.tail, "stdout")
	e.stderr = newLineWriter(e.tail, "stderr")
	cmd.Stdout = e.stdout
	cmd.Stderr = e.stderr

	started := make(chan error, 1)
	go e.supervise(started)
	if err := <-started; err != nil {
		return nil, fmt.Errorf("radiance: start engine %s: %w", path, err)
	}

	if err := e.waitReady(); err != nil {
		e.kill()
		return nil, err
	}
	return e, nil
}

// supervise starts the process and reaps it. It stays pinned to one OS thread
// for the life of the child: Linux ties PR_SET_PDEATHSIG to the thread that
// forked, so that thread must outlive the child.
func (e *engine) supervise(started chan<- error) {
	lockThreadForChild()
	err := e.cmd.Start()
	started <- err
	if err != nil {
		close(e.exited)
		return
	}
	e.waitErr = e.cmd.Wait()
	e.stdout.flush()
	e.stderr.flush()
	close(e.exited)
}

// mergeEnv lets the model's environment override the backend's own. Appending
// would not: on Linux getenv returns the first match, so the inherited value
// would win.
func mergeEnv(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		index[k] = len(out)
		out = append(out, kv)
	}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := index[k]; ok {
			out[i] = kv
			continue
		}
		index[k] = len(out)
		out = append(out, kv)
	}
	return out
}

// lineWriter turns a child stream into bounded log lines. Each stream has its
// own partial line, and the tail itself is shared and locked.
type lineWriter struct {
	mu      sync.Mutex
	tail    *tailBuffer
	stream  string
	partial []byte
}

func newLineWriter(tail *tailBuffer, stream string) *lineWriter {
	return &lineWriter{tail: tail, stream: stream}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.appendLocked(p)
			break
		}
		w.appendLocked(p[:i])
		w.flushLocked()
		p = p[i+1:]
	}
	return n, nil
}

func (w *lineWriter) appendLocked(b []byte) {
	if len(w.partial) >= engineTailBytes {
		return
	}
	if room := engineTailBytes - len(w.partial); len(b) > room {
		b = b[:room]
	}
	w.partial = append(w.partial, b...)
}

func (w *lineWriter) flushLocked() {
	if len(w.partial) == 0 {
		return
	}
	text := strings.TrimRight(string(w.partial), "\r")
	w.tail.add(w.stream, text)
	xlog.Debug("radiance engine", "stream", w.stream, "line", text)
	w.partial = w.partial[:0]
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (e *engine) waitReady() error {
	deadline := time.Now().Add(e.cfg.ReadyTimeout)
	interval := 100 * time.Millisecond
	for {
		select {
		case <-e.exited:
			return fmt.Errorf("radiance: engine exited before it was ready (%v)%s", e.waitErr, e.tailSuffix())
		default:
		}

		code, err := e.health()
		if err == nil && code == http.StatusOK {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("radiance: engine did not become ready within %s%s", e.cfg.ReadyTimeout, e.tailSuffix())
		}
		time.Sleep(interval)
		if interval < time.Second {
			interval *= 2
			if interval > time.Second {
				interval = time.Second
			}
		}
	}
}

func (e *engine) health() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.cfg.BaseURL, "/")+"/health", nil)
	if err != nil {
		return 0, err
	}
	resp, err := e.cfg.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

func (e *engine) tailSuffix() string {
	if t := e.tail.String(); t != "" {
		return ": " + t
	}
	return ""
}

func (e *engine) pid() int {
	if e.cmd != nil && e.cmd.Process != nil {
		return e.cmd.Process.Pid
	}
	return 0
}

func (e *engine) alive() bool {
	select {
	case <-e.exited:
		return false
	default:
		return true
	}
}

// kill ends the engine at once and waits for it to be reaped.
func (e *engine) kill() {
	e.mu.Lock()
	if e.killing {
		e.mu.Unlock()
		e.waitExit(engineReapTimeout)
		return
	}
	e.killing = true
	e.mu.Unlock()

	if e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	e.waitExit(engineReapTimeout)
}

func (e *engine) waitExit(timeout time.Duration) {
	select {
	case <-e.exited:
	case <-time.After(timeout):
		xlog.Warn("radiance: engine was killed but has not been reaped", "pid", e.pid(), "timeout", timeout)
	}
}

// close asks the engine to stop, then kills it after stop_timeout.
func (e *engine) close() {
	if !e.alive() {
		return
	}
	terminate(e.cmd)
	select {
	case <-e.exited:
	case <-time.After(e.cfg.StopTimeout):
		xlog.Warn("radiance: engine did not stop in time, killing it", "timeout", e.cfg.StopTimeout)
		e.kill()
	}
}

// tailBuffer keeps the last lines of the child's output. The backend runs for
// as long as the engine does, so the buffer must not grow with it.
type tailBuffer struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	maxBytes int
}

func (t *tailBuffer) add(stream, line string) {
	if len(line) > t.maxBytes {
		line = line[:t.maxBytes] + "..."
	}
	entry := stream + ": " + line
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, entry)
	if len(t.lines) > t.maxLines {
		t.lines = t.lines[len(t.lines)-t.maxLines:]
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
